package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/history"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// runAgent is the entry point for an agent worker process.
// It reads a SpawnConfig from stdin, sets up the agent runtime,
// and starts a gRPC server for the control plane to communicate with.
func runAgent() error {
	// Read spawn config from stdin
	var cfg ipc.SpawnConfig
	if err := json.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		return fmt.Errorf("reading spawn config: %w", err)
	}

	// When running under UID isolation, set a collaborative umask and
	// verify we are running as the expected user.
	if cfg.UID != 0 {
		syscall.Umask(0002) // files: 0664, dirs: 0775 (group-writable)
		if uint32(os.Getuid()) != cfg.UID {
			return fmt.Errorf("expected to run as UID %d, but running as UID %d", cfg.UID, os.Getuid())
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger = logger.With("agent", cfg.AgentName, "session", cfg.SessionID)

	// Load agent definition
	agentCfg, err := config.LoadAgentDir(cfg.AgentDefDir)
	if err != nil {
		return fmt.Errorf("loading agent definition: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create language model
	model := agentCfg.Model
	if cfg.Model != "" {
		model = cfg.Model
	}
	// API key is passed via env var (not in SpawnConfig JSON) for security.
	apiKey := os.Getenv("HIVE_API_KEY")
	lm, err := agent.CreateLanguageModel(ctx, agent.ProviderType(cfg.Provider), apiKey, model)
	if err != nil {
		return fmt.Errorf("creating language model: %w", err)
	}

	// Connect to control plane gRPC for manager tool calls
	hostConn, err := grpc.NewClient("unix://"+cfg.HostSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("connecting to control plane: %w", err)
	}
	defer hostConn.Close()
	host := grpcipc.NewHostClient(hostConn, cfg.SessionID)

	// Build agent options
	opts := agent.Options{
		WorkingDir:     cfg.WorkingDir,
		SessionDir:     cfg.SessionDir,
		AgentDefDir:    cfg.AgentDefDir,
		SharedSkillDir: cfg.SharedSkillDir,
		LM:             lm,
		AllowedTools:   cfg.EffectiveTools,
		HasSkills:      cfg.EffectiveTools["use_skill"],
	}

	// All agents can spawn ephemeral subagents. Spawn and coordinator tools
	// are structural capabilities gated by mode, not subject to operator
	// override via config.yaml tool allowlists. The mode itself is the gate.
	opts.ExtraTools = append(opts.ExtraTools, agent.BuildSpawnTool(host))

	// Coordinator agents get persistent agent management tools.
	if cfg.Mode == string(config.ModeCoordinator) {
		opts.ExtraTools = append(opts.ExtraTools, agent.BuildCoordinatorTools(host)...)
	}

	// Inject secret functions only if the agent has bash — secrets are
	// injected as env vars in bash commands, so they're useless without it.
	// nil EffectiveTools means unrestricted (all tools allowed, including bash).
	if cfg.EffectiveTools == nil || cfg.EffectiveTools["bash"] {
		opts.SecretEnvFn = func() []string {
			_, env, err := host.GetSecrets(context.Background())
			if err != nil {
				logger.Warn("failed to fetch secrets", "error", err)
				return nil
			}
			return env
		}
		opts.SecretNamesFn = func() []string {
			names, _, err := host.GetSecrets(context.Background())
			if err != nil {
				logger.Warn("failed to fetch secret names", "error", err)
				return nil
			}
			return names
		}
	}

	// Read identity if present
	if id, err := config.ReadOptionalFile(filepath.Join(cfg.SessionDir, "identity.md")); err == nil {
		opts.Identity = id
	}

	// Set up persistent agent tools and conversation
	var conv *agent.Conversation
	var historyEngine *history.Engine

	if config.AgentMode(cfg.Mode).IsPersistent() {
		opts.ExtraTools = append(opts.ExtraTools, agent.BuildMemoryTools(cfg.SessionDir)...)
		opts.ExtraTools = append(opts.ExtraTools, agent.BuildTodoTools(cfg.SessionDir)...)

		historyPath := filepath.Join(cfg.SessionDir, "db", "history.db")
		store, storeErr := history.OpenStore(historyPath)
		if storeErr != nil {
			logger.Warn("failed to open history DB, using ephemeral conversation", "error", storeErr)
			conv = agent.NewConversation()
		} else {
			historyEngine = history.NewEngine(store, lm, history.DefaultConfig(), logger)
			opts.ExtraTools = append(opts.ExtraTools, agent.BuildHistoryTools(historyEngine)...)
			conv = agent.NewConversationWithHistory(historyEngine)
		}
	} else {
		conv = agent.NewConversation()
	}

	// Create the agent
	a, err := agent.New(ctx, agentCfg, opts, logger)
	if err != nil {
		return fmt.Errorf("creating agent: %w", err)
	}

	// Create the worker (implements ipc.AgentWorker)
	worker := &agentWorker{
		agent:  a,
		conv:   conv,
		cancel: cancel,
		logger: logger,
	}

	// Start gRPC server on Unix socket
	socketPath := cfg.AgentSocket
	if socketPath == "" {
		socketPath = fmt.Sprintf("/tmp/hive-agent-%s.sock", cfg.SessionID)
	}
	os.Remove(socketPath) // clean up stale socket
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	defer os.Remove(socketPath)

	srv := grpc.NewServer()
	grpcipc.NewWorkerServer(worker).Register(srv)

	go func() {
		if err := srv.Serve(lis); err != nil {
			logger.Error("gRPC server error", "error", err)
			cancel()
		}
	}()

	// Signal ready to the control plane
	fmt.Fprintln(os.Stdout, "ready")

	logger.Info("agent worker ready")

	// Block until shutdown
	<-ctx.Done()
	srv.GracefulStop()
	a.Cleanup()
	if historyEngine != nil {
		historyEngine.Close()
	}
	logger.Info("agent worker stopped")
	return nil
}

// agentWorker implements ipc.AgentWorker for a single agent process.
type agentWorker struct {
	agent  *agent.Agent
	conv   *agent.Conversation
	cancel context.CancelFunc
	logger *slog.Logger
}

func (w *agentWorker) Chat(ctx context.Context, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	return w.agent.StreamChat(ctx, w.conv, message, onEvent)
}

func (w *agentWorker) Shutdown(ctx context.Context) error {
	w.logger.Info("shutdown requested")
	w.cancel()
	return nil
}
