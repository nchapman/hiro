package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/agent"
	"github.com/nchapman/hiro/internal/agent/tools"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/ipc/grpcipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
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

	// Secret env vars are received from the control plane with each tool call.
	// Store the latest set atomically so the BackgroundJobManager can read them.
	var secretEnvMu sync.Mutex
	var secretEnv []string

	// When running under UID isolation, enable additional security:
	// 1. Confine file tools to the platform root (/hiro) — prevents reading/writing
	//    outside the workspace (e.g. /opt/mise, /etc, other instance dirs).
	// 2. Block SSRF in fetch — prevents hitting cloud metadata (169.254.169.254)
	//    or internal services.
	// Note: the bash tool is not confinable here — it relies on UID/group DAC.
	if cfg.UID != 0 {
		tools.SetAllowedRoots([]string{cfg.WorkingDir})
		tools.SetSSRFProtection(true)
	}

	bgMgr := tools.NewBackgroundJobManager(func() []string {
		secretEnvMu.Lock()
		defer secretEnvMu.Unlock()
		return secretEnv
	})

	// Wire background job completion events to a channel for the gRPC stream.
	completions := make(chan *pb.JobCompletion, 64)
	bgMgr.OnComplete = func(job *tools.BackgroundJob) {
		exitCode := int32(0)
		failed := false
		if job.ExitErr() != nil {
			failed = true
			var e *exec.ExitError
			if errors.As(job.ExitErr(), &e) {
				exitCode = int32(e.ExitCode())
			}
		}
		select {
		case completions <- &pb.JobCompletion{
			TaskId:      job.ID,
			Command:     job.Command,
			Description: job.Description,
			ExitCode:    exitCode,
			Failed:      failed,
		}:
		default:
			logger.Warn("job completion dropped (channel full)", "task_id", job.ID)
		}
	}

	toolSet := buildWorkerTools(cfg.WorkingDir, bgMgr, cfg.EffectiveTools)

	executor := agent.ToolExecutorFromTools(toolSet)

	worker := &toolWorker{
		executor: executor,
		cancel:   cancel,
		logger:   logger,
	}

	// Start gRPC server on Unix socket.
	socketPath := cfg.AgentSocket
	if socketPath == "" {
		socketPath = fmt.Sprintf("/tmp/hiro-agent-%s.sock", cfg.SessionID)
	}
	os.Remove(socketPath)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	defer os.Remove(socketPath)

	srv := grpc.NewServer()
	ws := grpcipc.NewWorkerServer(worker)
	ws.SetSecretEnvCallback(func(env []string) {
		secretEnvMu.Lock()
		secretEnv = env
		secretEnvMu.Unlock()
	})
	ws.SetCompletionChannel(completions)
	ws.Register(srv)

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
		tools.NewReadTool(workingDir),
		tools.NewEditTool(workingDir),
		tools.NewWriteTool(workingDir),
		tools.NewGlobTool(workingDir),
		tools.NewGrepTool(workingDir),
		tools.NewWebFetchTool(),
		tools.NewTaskOutputTool(bgMgr),
		tools.NewTaskStopTool(bgMgr),
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
