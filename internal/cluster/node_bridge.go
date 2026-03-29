package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	pb "github.com/nchapman/hivebot/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const workerSpawnTimeout = 30 * time.Second

// NodeBridge manages local worker processes on a worker node. It receives
// commands from the leader stream and translates them into local process
// operations using the same hive agent spawn protocol.
type NodeBridge struct {
	rootDir string
	stream  *WorkerStream
	logger  *slog.Logger

	mu      sync.Mutex
	workers map[string]*localWorker // session ID → local worker
}

// localWorker tracks a locally spawned hive agent process.
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

// handleSpawn spawns a local hive agent process.
func (nb *NodeBridge) handleSpawn(ctx context.Context, msg *pb.SpawnWorker) {
	nb.logger.Info("spawning worker", "instance_id", msg.InstanceId, "session_id", msg.SessionId, "agent", msg.AgentName)

	// Translate paths to local filesystem.
	workingDir := filepath.Join(nb.rootDir, msg.WorkingDir)
	sessionDir := filepath.Join(nb.rootDir, msg.SessionDir)

	// Create directories.
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		nb.logger.Error("creating session dir", "error", err)
		nb.stream.SendSpawnResult(msg.RequestId, fmt.Sprintf("creating session dir: %v", err))
		return
	}
	for _, sub := range []string{"scratch", "tmp"} {
		if err := os.MkdirAll(filepath.Join(sessionDir, sub), 0755); err != nil {
			nb.logger.Error("creating session subdir", "dir", sub, "error", err)
		}
	}

	cfg := ipc.SpawnConfig{
		InstanceID:     msg.InstanceId,
		SessionID:      msg.SessionId,
		AgentName:      msg.AgentName,
		EffectiveTools: msg.EffectiveTools,
		WorkingDir:     workingDir,
		SessionDir:     sessionDir,
	}

	handle, err := nb.spawnLocalWorker(ctx, cfg)
	if err != nil {
		nb.logger.Error("spawning worker", "error", err)
		nb.stream.SendSpawnResult(msg.RequestId, err.Error())
		return
	}

	nb.mu.Lock()
	nb.workers[msg.SessionId] = handle
	nb.mu.Unlock()

	// Watch for unexpected worker exit.
	go func() {
		<-handle.done
		nb.mu.Lock()
		delete(nb.workers, msg.SessionId)
		nb.mu.Unlock()
		nb.stream.SendWorkerExited(msg.SessionId, "")
		nb.logger.Info("worker exited", "session_id", msg.SessionId)
	}()

	nb.stream.SendSpawnResult(msg.RequestId, "")
	nb.logger.Info("worker spawned", "session_id", msg.SessionId)
}

// handleExecuteTool forwards a tool call to the local worker.
func (nb *NodeBridge) handleExecuteTool(ctx context.Context, msg *pb.ExecuteToolRemote) {
	nb.mu.Lock()
	w, ok := nb.workers[msg.SessionId]
	nb.mu.Unlock()

	if !ok {
		nb.stream.SendToolResult(msg.SessionId, msg.CallId, "", false, fmt.Sprintf("no worker for session %s", msg.SessionId))
		return
	}

	result, err := w.worker.ExecuteTool(ctx, msg.CallId, msg.ToolName, msg.Input)
	if err != nil {
		nb.stream.SendToolResult(msg.SessionId, msg.CallId, "", false, err.Error())
		return
	}

	nb.stream.SendToolResult(msg.SessionId, msg.CallId, result.Content, result.IsError, "")
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
	for k, v := range nb.workers {
		workers[k] = v
	}
	nb.mu.Unlock()

	for sessionID, w := range workers {
		nb.logger.Info("shutting down worker", "session_id", sessionID)
		if err := w.worker.Shutdown(ctx); err != nil {
			w.kill()
		}
		w.close()
	}
}

// spawnLocalWorker spawns a hive agent process locally using the same
// protocol as the leader's defaultWorkerFactory.
func (nb *NodeBridge) spawnLocalWorker(ctx context.Context, cfg ipc.SpawnConfig) (*localWorker, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving executable: %w", err)
	}

	socketPath := fmt.Sprintf("/tmp/hive-agent-%s.sock", cfg.SessionID)
	cfg.AgentSocket = socketPath

	cmd := exec.CommandContext(ctx, self, "agent")
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	// Write spawn config.
	if err := json.NewEncoder(stdinPipe).Encode(cfg); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("writing spawn config: %w", err)
	}
	stdinPipe.Close()

	// Wait for readiness.
	readyCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := stdoutPipe.Read(buf)
		if err != nil {
			readyCh <- fmt.Errorf("reading ready signal: %w", err)
			return
		}
		if string(buf[:n]) != "ready\n" && string(buf[:n]) != "ready" {
			readyCh <- fmt.Errorf("unexpected ready signal: %q", string(buf[:n]))
			return
		}
		readyCh <- nil
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			cmd.Process.Kill()
			return nil, err
		}
	case <-time.After(workerSpawnTimeout):
		cmd.Process.Kill()
		return nil, fmt.Errorf("worker did not become ready within %v", workerSpawnTimeout)
	}

	// Connect gRPC client.
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("connecting to worker: %w", err)
	}

	worker := grpcipc.NewWorkerClient(conn)

	// Track process exit.
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()

	return &localWorker{
		worker: worker,
		kill: func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		},
		close: func() {
			conn.Close()
			os.Remove(socketPath)
		},
		done: done,
	}, nil
}
