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
	"github.com/nchapman/hiro/internal/platform/fsperm"
	"google.golang.org/grpc"
)

const (
	// umaskCollaborative allows group writes for UID-isolated agents.
	umaskCollaborative = 0o002

	// jobCompletionBufSize is the channel buffer for background job completions.
	jobCompletionBufSize = 64
)

// runAgent is the entry point for an agent worker process.
// Workers are thin tool-execution sandboxes: they receive ExecuteTool
// RPCs from the control plane and execute tools under an isolated UID.
//
// When network isolation is active (PeerName set in config), the startup
// follows a two-phase handshake:
//  1. Signal "ns-ready" (namespaces up, awaiting veth from parent)
//  2. Wait for veth-ready signal (FD 3 closed by parent)
//  3. Self-configure network (interfaces, routes, bind mounts)
//  4. Install seccomp-BPF filter (blocks namespace creation, mounts)
//  5. Signal "ready" (gRPC server listening)
//
// All UID-isolated agents (not just network-isolated) get the seccomp filter
// to prevent namespace creation, mount manipulation, etc.
func runAgent() error {
	var cfg ipc.SpawnConfig
	if err := json.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		return fmt.Errorf("reading spawn config: %w", err)
	}

	// Network isolation handshake: self-configure inside user namespace.
	if cfg.PeerName != "" {
		// Signal that namespaces are up and we're ready for the veth peer.
		fmt.Fprintln(os.Stdout, "ns-ready")

		// Block until parent has created veth and moved peer into our netns.
		waitForVethReady()

		// Self-configure: rename peer to eth0, set IP/route, bind-mount DNS.
		if err := selfConfigureNetwork(cfg); err != nil {
			return fmt.Errorf("self-configuring network: %w", err)
		}
	}

	// For UID-isolated agents: activate supplementary groups (e.g.,
	// hiro-operators for agents/ write access) then lock down with seccomp.
	// Both must run AFTER self-configuration (which needs mount) but
	// BEFORE any agent code.
	if cfg.UID != 0 {
		if err := activateGroups(cfg.Groups); err != nil {
			return fmt.Errorf("activating supplementary groups: %w", err)
		}
		if err := installSeccomp(); err != nil {
			return fmt.Errorf("installing seccomp filter: %w", err)
		}
	}

	if err := configureAgentSecurity(cfg); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger = logger.With("agent", cfg.AgentName, "session", cfg.SessionID)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	bg := setupBackgroundJobs(logger)
	toolSet := buildWorkerTools(cfg.WorkingDir, bg.mgr, cfg.EffectiveTools)
	executor := agent.ToolExecutorFromTools(toolSet)

	worker := &toolWorker{
		executor: executor,
		cancel:   cancel,
		logger:   logger,
	}

	srv, cleanup, err := startAgentGRPC(cfg, worker, bg, cancel, logger)
	if err != nil {
		return err
	}
	defer cleanup()

	// Signal ready to the control plane.
	fmt.Fprintln(os.Stdout, "ready")
	logger.Info("agent worker ready")

	<-ctx.Done()
	srv.GracefulStop()
	bg.mgr.KillAll()
	logger.Info("agent worker stopped")
	return nil
}

// configureAgentSecurity sets up UID isolation when running under the Unix user
// pool. This includes: collaborative umask, UID verification, file tool
// confinement to the platform root, and SSRF protection against cloud metadata
// endpoints. The Bash tool is not confinable here — it relies on UID/group DAC.
func configureAgentSecurity(cfg ipc.SpawnConfig) error {
	if cfg.UID == 0 {
		return nil
	}

	syscall.Umask(umaskCollaborative)

	// With CLONE_NEWUSER, os.Getuid() returns 0 inside the user namespace
	// (mapped to the agent UID outside). All UID-isolated agents use namespaces.
	actualUID := uint32(os.Getuid()) //nolint:gosec // UID fits uint32 on all supported platforms
	if actualUID != 0 {
		return fmt.Errorf("expected to run as UID 0 inside user namespace, but running as UID %d", actualUID)
	}

	// Confine file tools to the platform root — prevents reading/writing
	// outside the workspace. Block SSRF to prevent hitting cloud metadata
	// or internal services.
	tools.SetAllowedRoots([]string{cfg.WorkingDir})
	tools.SetSSRFProtection(true)
	return nil
}

// backgroundJobs bundles the background job manager with its completion channel
// and secret env callback for wiring into the gRPC server.
type backgroundJobs struct {
	mgr              *tools.BackgroundJobManager
	completions      chan *pb.JobCompletion
	setSecretEnvFunc func([]string)
}

// setupBackgroundJobs creates the background job manager and wires completion
// events to a channel for the gRPC stream. Secret env vars are stored
// atomically so background jobs inherit the latest set from each tool call.
func setupBackgroundJobs(logger *slog.Logger) backgroundJobs {
	// Secret env vars are received from the control plane with each tool call.
	var secretEnvMu sync.Mutex
	var secretEnv []string

	bgMgr := tools.NewBackgroundJobManager(func() []string {
		secretEnvMu.Lock()
		defer secretEnvMu.Unlock()
		return secretEnv
	})

	completions := make(chan *pb.JobCompletion, jobCompletionBufSize)
	bgMgr.OnComplete = func(job *tools.BackgroundJob) {
		exitCode := int32(0)
		failed := false
		if job.ExitErr() != nil {
			failed = true
			var e *exec.ExitError
			if errors.As(job.ExitErr(), &e) {
				exitCode = int32(e.ExitCode()) //nolint:gosec // exit codes fit int32
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

	return backgroundJobs{
		mgr:         bgMgr,
		completions: completions,
		setSecretEnvFunc: func(env []string) {
			secretEnvMu.Lock()
			secretEnv = env
			secretEnvMu.Unlock()
		},
	}
}

// startAgentGRPC creates and starts the gRPC server on a Unix socket for
// receiving ExecuteTool RPCs from the control plane. Returns the server (for
// GracefulStop) and a cleanup function that removes the socket file. The cancel
// func is called if the gRPC server encounters a fatal error, unblocking the
// caller's ctx.Done() wait.
func startAgentGRPC(cfg ipc.SpawnConfig, worker ipc.AgentWorker, bg backgroundJobs, cancel context.CancelFunc, logger *slog.Logger) (*grpc.Server, func(), error) {
	socketPath := cfg.AgentSocket
	if socketPath == "" {
		socketPath = fmt.Sprintf("/tmp/hiro-agent-%s.sock", cfg.SessionID)
	}
	os.Remove(socketPath)
	lis, err := net.Listen("unix", socketPath) //nolint:noctx // startup-time listener, no cancellation needed
	if err != nil {
		return nil, nil, fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	// Restrict socket permissions so only the owning UID can connect.
	// Defense in depth: the socket directory is already 0700, but an
	// explicit chmod prevents cross-agent access if dir perms ever loosen.
	os.Chmod(socketPath, fsperm.FilePrivate) //nolint:errcheck // defense in depth: socket dir is 0700, so failure here is non-fatal
	cleanup := func() { os.Remove(socketPath) }

	srv := grpc.NewServer()
	ws := grpcipc.NewWorkerServer(worker)
	ws.SetSecretEnvCallback(bg.setSecretEnvFunc)
	ws.SetCompletionChannel(bg.completions)
	ws.Register(srv)

	go func() {
		if err := srv.Serve(lis); err != nil {
			logger.Error("gRPC server error", "error", err)
			cancel()
		}
	}()

	return srv, cleanup, nil
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
