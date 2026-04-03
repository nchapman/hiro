package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/ipc/grpcipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"github.com/nchapman/hiro/internal/platform/fsperm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const workerSpawnTimeout = 30 * time.Second

// NodeBridge manages local worker processes on a worker node. It receives
// commands from the leader stream and translates them into local process
// operations using the same hiro agent spawn protocol.
type NodeBridge struct {
	rootDir string
	stream  *WorkerStream
	logger  *slog.Logger

	mu      sync.Mutex
	workers map[string]*localWorker // session ID → local worker
}

// localWorker tracks a locally spawned hiro agent process.
type localWorker struct {
	worker ipc.AgentWorker
	kill   func()
	close  func()
	done   <-chan struct{}
}

// NewNodeBridge creates a new node bridge that manages local workers.
func NewNodeBridge(rootDir string, stream *WorkerStream, logger *slog.Logger) *NodeBridge {
	nb := &NodeBridge{
		rootDir: rootDir,
		stream:  stream,
		logger:  logger,
		workers: make(map[string]*localWorker),
	}

	// Wire up handlers.
	stream.SetSpawnHandler(nb.handleSpawn)
	stream.SetExecuteToolHandler(nb.handleExecuteTool)
	stream.SetShutdownWorkerHandler(nb.handleShutdown)
	stream.SetKillWorkerHandler(nb.handleKill)

	return nb
}

// handleSpawn spawns a local hiro agent process.
func (nb *NodeBridge) handleSpawn(ctx context.Context, msg *pb.SpawnWorker) {
	nb.logger.Info("spawning worker", "instance_id", msg.InstanceId, "session_id", msg.SessionId, "agent", msg.AgentName)

	cfg, err := nb.prepareSpawnConfig(msg)
	if err != nil {
		nb.logger.Error("preparing spawn", "error", err)
		_ = nb.stream.SendSpawnResult(msg.RequestId, err.Error())
		return
	}

	handle, err := nb.spawnLocalWorker(ctx, cfg)
	if err != nil {
		nb.logger.Error("spawning worker", "error", err)
		_ = nb.stream.SendSpawnResult(msg.RequestId, err.Error())
		return
	}

	nb.mu.Lock()
	nb.workers[msg.SessionId] = handle
	nb.mu.Unlock()

	// Watch for unexpected worker exit.
	go nb.watchWorkerExit(msg.SessionId, handle)

	// Forward background job completions from this worker to the leader.
	if wc, ok := handle.worker.(*grpcipc.WorkerClient); ok {
		go nb.forwardJobCompletions(ctx, msg.SessionId, wc)
	}

	_ = nb.stream.SendSpawnResult(msg.RequestId, "")
	nb.logger.Info("worker spawned", "session_id", msg.SessionId)
}

// prepareSpawnConfig translates a SpawnWorker message into a local SpawnConfig,
// creating the required session directories.
func (nb *NodeBridge) prepareSpawnConfig(msg *pb.SpawnWorker) (ipc.SpawnConfig, error) {
	workingDir := nb.rootDir
	if msg.WorkingDir != "" && msg.WorkingDir != "." {
		workingDir = filepath.Join(nb.rootDir, msg.WorkingDir)
	}
	sessionDir := filepath.Join(nb.rootDir, msg.SessionDir)

	if err := os.MkdirAll(sessionDir, fsperm.DirStandard); err != nil {
		return ipc.SpawnConfig{}, fmt.Errorf("creating session dir: %w", err)
	}
	for _, sub := range []string{"scratch", "tmp"} {
		if err := os.MkdirAll(filepath.Join(sessionDir, sub), fsperm.DirStandard); err != nil {
			nb.logger.Error("creating session subdir", "dir", sub, "error", err)
		}
	}

	return ipc.SpawnConfig{
		InstanceID:     msg.InstanceId,
		SessionID:      msg.SessionId,
		AgentName:      msg.AgentName,
		EffectiveTools: msg.EffectiveTools,
		WorkingDir:     workingDir,
		SessionDir:     sessionDir,
	}, nil
}

// watchWorkerExit monitors a worker's done channel and cleans up on exit.
func (nb *NodeBridge) watchWorkerExit(sessionID string, handle *localWorker) {
	<-handle.done
	nb.mu.Lock()
	delete(nb.workers, sessionID)
	nb.mu.Unlock()
	_ = nb.stream.SendWorkerExited(sessionID, "")
	nb.logger.Info("worker exited", "session_id", sessionID)
}

// forwardJobCompletions relays background job completion notifications
// from a local worker to the leader.
func (nb *NodeBridge) forwardJobCompletions(ctx context.Context, sessionID string, wc *grpcipc.WorkerClient) {
	ch := wc.WatchJobs(ctx, nb.logger)
	for c := range ch {
		if err := nb.stream.Send(&pb.NodeMessage{
			Msg: &pb.NodeMessage_JobCompletion{
				JobCompletion: &pb.JobCompletionNotify{
					SessionId:   sessionID,
					TaskId:      c.TaskId,
					Command:     c.Command,
					Description: c.Description,
					ExitCode:    c.ExitCode,
					Failed:      c.Failed,
				},
			},
		}); err != nil {
			nb.logger.Debug("failed to forward job completion", "session_id", sessionID, "error", err)
			return
		}
	}
}

// handleExecuteTool forwards a tool call to the local worker.
func (nb *NodeBridge) handleExecuteTool(ctx context.Context, msg *pb.ExecuteToolRemote) {
	nb.mu.Lock()
	w, ok := nb.workers[msg.SessionId]
	nb.mu.Unlock()

	if !ok {
		_ = nb.stream.SendToolResult(msg.SessionId, msg.CallId, "", false, fmt.Sprintf("no worker for session %s", msg.SessionId))
		return
	}

	result, err := w.worker.ExecuteTool(ctx, msg.CallId, msg.ToolName, msg.Input)
	if err != nil {
		_ = nb.stream.SendToolResult(msg.SessionId, msg.CallId, "", false, err.Error())
		return
	}

	_ = nb.stream.SendToolResult(msg.SessionId, msg.CallId, result.Content, result.IsError, "")
}

// handleShutdown gracefully shuts down a local worker.
func (nb *NodeBridge) handleShutdown(ctx context.Context, msg *pb.ShutdownWorker) {
	nb.mu.Lock()
	w, ok := nb.workers[msg.SessionId]
	nb.mu.Unlock()

	if !ok {
		return
	}

	nb.logger.Info("shutting down worker", "session_id", msg.SessionId)
	if err := w.worker.Shutdown(ctx); err != nil {
		nb.logger.Warn("shutdown error", "session_id", msg.SessionId, "error", err)
	}
}

// handleKill force-kills a local worker.
func (nb *NodeBridge) handleKill(ctx context.Context, msg *pb.KillWorker) {
	nb.mu.Lock()
	w, ok := nb.workers[msg.SessionId]
	nb.mu.Unlock()

	if !ok {
		return
	}

	nb.logger.Info("killing worker", "session_id", msg.SessionId)
	w.kill()
}

// ShutdownAll stops all local workers gracefully.
func (nb *NodeBridge) ShutdownAll(ctx context.Context) {
	nb.mu.Lock()
	workers := make(map[string]*localWorker, len(nb.workers))
	maps.Copy(workers, nb.workers)
	nb.mu.Unlock()

	for sessionID, w := range workers {
		nb.logger.Info("shutting down worker", "session_id", sessionID)
		if err := w.worker.Shutdown(ctx); err != nil {
			w.kill()
		}
		w.close()
	}
}

// spawnLocalWorker spawns a hiro agent process locally using the same
// protocol as the leader's defaultWorkerFactory.
func (nb *NodeBridge) spawnLocalWorker(ctx context.Context, cfg ipc.SpawnConfig) (*localWorker, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving executable: %w", err)
	}

	socketDir, socketPath, err := nb.createWorkerSocket(cfg.SessionID)
	if err != nil {
		return nil, err
	}
	cfg.AgentSocket = socketPath

	cmd, stdoutPipe, err := nb.startWorkerProcess(ctx, self, cfg)
	if err != nil {
		return nil, err
	}

	if err := nb.awaitWorkerReady(stdoutPipe, cmd); err != nil {
		return nil, err
	}

	// Connect gRPC client.
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("connecting to worker: %w", err)
	}

	worker := grpcipc.NewWorkerClient(conn)

	// Track process exit.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	return &localWorker{
		worker: worker,
		kill: func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		},
		close: func() {
			conn.Close()
			os.Remove(socketPath)
			os.Remove(socketDir)
		},
		done: done,
	}, nil
}

// createWorkerSocket creates a private socket directory and returns the
// directory path and socket path.
func (nb *NodeBridge) createWorkerSocket(sessionID string) (socketDir, socketPath string, err error) {
	sessPrefix := sessionID
	if len(sessPrefix) > ipc.MaxSessionPrefix {
		sessPrefix = sessPrefix[:ipc.MaxSessionPrefix]
	}
	socketDir = fmt.Sprintf("/tmp/hiro-%s", sessPrefix)
	if err := os.MkdirAll(socketDir, fsperm.DirPrivate); err != nil {
		return "", "", fmt.Errorf("creating socket dir: %w", err)
	}
	return socketDir, socketDir + "/a.sock", nil
}

// startWorkerProcess starts a hiro agent subprocess, writes the spawn config
// to its stdin, and returns the command and stdout pipe for ready-signal reading.
func (nb *NodeBridge) startWorkerProcess(ctx context.Context, executable string, cfg ipc.SpawnConfig) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, executable, "agent")
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting agent: %w", err)
	}

	if err := json.NewEncoder(stdinPipe).Encode(cfg); err != nil {
		_ = cmd.Process.Kill()
		return nil, nil, fmt.Errorf("writing spawn config: %w", err)
	}
	_ = stdinPipe.Close()

	return cmd, stdoutPipe, nil
}

// awaitWorkerReady waits for the worker to print "ready" on stdout.
// Kills the process on timeout or unexpected signal.
func (nb *NodeBridge) awaitWorkerReady(stdoutPipe io.ReadCloser, cmd *exec.Cmd) error {
	readyCh := make(chan error, 1)
	go func() {
		buf := make([]byte, readyBufSize)
		n, err := stdoutPipe.Read(buf)
		if err != nil {
			readyCh <- fmt.Errorf("reading ready signal: %w", err)
			return
		}
		if n == 0 {
			readyCh <- fmt.Errorf("empty ready signal")
			return
		}
		sig := strings.TrimSpace(string(buf[:n]))
		if sig != "ready" {
			readyCh <- fmt.Errorf("unexpected ready signal: %q", sig)
			return
		}
		readyCh <- nil
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			_ = cmd.Process.Kill()
			return err
		}
		return nil
	case <-time.After(workerSpawnTimeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("worker did not become ready within %v", workerSpawnTimeout)
	}
}
