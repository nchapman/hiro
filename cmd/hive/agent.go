package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/agent"
	"github.com/nchapman/hivebot/internal/agent/tools"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	"google.golang.org/grpc"
)

// runAgent is the entry point for an agent worker process.
// Workers are thin tool-execution sandboxes: they receive ExecuteTool
// RPCs from the control plane and execute tools under an isolated UID.
func runAgent() error {
	var cfg ipc.SpawnConfig
	if err := json.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		return fmt.Errorf("reading spawn config: %w", err)
	}

	// When running under UID isolation, set a collaborative umask and
	// verify we are running as the expected user.
	if cfg.UID != 0 {
		syscall.Umask(0002)
		if uint32(os.Getuid()) != cfg.UID {
			return fmt.Errorf("expected to run as UID %d, but running as UID %d", cfg.UID, os.Getuid())
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger = logger.With("agent", cfg.AgentName, "session", cfg.SessionID)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Build sandboxed tools (file ops, bash, etc.).
	bgMgr := tools.NewBackgroundJobManager(nil)
	toolSet := buildWorkerTools(cfg.WorkingDir, bgMgr, cfg.EffectiveTools)

	// Create tool executor from the tool set.
	executor := agent.ToolExecutorFromTools(toolSet)

	// Create a minimal AgentWorker that delegates to the executor.
	worker := &toolWorker{
		executor: executor,
		cancel:   cancel,
		logger:   logger,
	}

	// Start gRPC server on Unix socket.
	socketPath := cfg.AgentSocket
	if socketPath == "" {
		socketPath = fmt.Sprintf("/tmp/hive-agent-%s.sock", cfg.SessionID)
	}
	os.Remove(socketPath)
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

	// Signal ready to the control plane.
	fmt.Fprintln(os.Stdout, "ready")
	logger.Info("agent worker ready")

	<-ctx.Done()
	srv.GracefulStop()
	bgMgr.KillAll()
	logger.Info("agent worker stopped")
	return nil
}

// toolWorker implements ipc.AgentWorker as a thin tool executor.
type toolWorker struct {
	executor ipc.ToolExecutor
	cancel   context.CancelFunc
	logger   *slog.Logger
}

func (w *toolWorker) ExecuteTool(ctx context.Context, callID, name, input string) (ipc.ToolResult, error) {
	return w.executor.ExecuteTool(ctx, callID, name, input)
}

func (w *toolWorker) Shutdown(ctx context.Context) error {
	w.logger.Info("shutdown requested")
	w.cancel()
	return nil
}

// buildWorkerTools creates the set of sandboxed tools available to the worker.
func buildWorkerTools(workingDir string, bgMgr *tools.BackgroundJobManager, allowed map[string]bool) []fantasy.AgentTool {
	all := []fantasy.AgentTool{
		tools.NewBashTool(workingDir, bgMgr),
		tools.NewReadFileTool(workingDir),
		tools.NewEditTool(workingDir),
		tools.NewMultiEditTool(workingDir),
		tools.NewWriteFileTool(workingDir),
		tools.NewListFilesTool(workingDir),
		tools.NewGlobTool(workingDir),
		tools.NewGrepTool(workingDir),
		tools.NewFetchTool(),
		tools.NewJobOutputTool(bgMgr),
		tools.NewJobKillTool(bgMgr),
	}

	if allowed == nil {
		return all
	}

	filtered := make([]fantasy.AgentTool, 0, len(all))
	for _, t := range all {
		if allowed[t.Info().Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
