package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const spawnTimeout = 30 * time.Second

// defaultWorkerFactory spawns an agent as an OS process using the same binary.
// The agent process reads a SpawnConfig from stdin, starts a gRPC AgentWorker
// server on a Unix socket, and writes "ready" to stdout when it's listening.
func defaultWorkerFactory(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving executable: %w", err)
	}

	// Deterministic socket path — the agent listens here.
	socketPath := fmt.Sprintf("/tmp/hive-agent-%s.sock", cfg.SessionID)
	cfg.AgentSocket = socketPath

	cmd := exec.CommandContext(ctx, self, "agent")

	// Pass API key via env var rather than JSON payload to avoid
	// it being visible in /proc/<pid>/fd/0 or accidentally logged.
	cmd.Env = append(os.Environ(), "HIVE_API_KEY="+cfg.APIKey)
	cfg.APIKey = "" // strip from JSON payload

	// Pipe SpawnConfig as JSON to stdin.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}

	// Capture stdout for the readiness signal.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Capture stderr for error diagnostics.
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent process: %w", err)
	}

	// Write config to stdin and close.
	if err := json.NewEncoder(stdinPipe).Encode(cfg); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("writing spawn config: %w", err)
	}
	stdinPipe.Close()

	// Wait for readiness: select on stdout readline, cmd.Wait (early exit), and timeout.
	type readyResult struct {
		err error
	}
	readyCh := make(chan readyResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		if scanner.Scan() {
			readyCh <- readyResult{}
		} else {
			readyCh <- readyResult{err: fmt.Errorf("agent process closed stdout without signaling ready")}
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case r := <-readyCh:
		if r.err != nil {
			cmd.Process.Kill()
			<-waitCh
			return nil, fmt.Errorf("%w; stderr: %s", r.err, stderrBuf.String())
		}
	case waitErr := <-waitCh:
		// Process exited before signaling ready.
		return nil, fmt.Errorf("agent process exited during startup: %v; stderr: %s", waitErr, stderrBuf.String())
	case <-time.After(spawnTimeout):
		cmd.Process.Kill()
		<-waitCh
		return nil, fmt.Errorf("agent process startup timed out after %s; stderr: %s", spawnTimeout, stderrBuf.String())
	}

	// Connect gRPC client to the agent's worker socket.
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cmd.Process.Kill()
		<-waitCh
		return nil, fmt.Errorf("connecting to agent worker: %w", err)
	}

	worker := grpcipc.NewWorkerClient(conn)

	// Death-watcher: closes done when the process exits.
	// The wait goroutine (line ~79) calls cmd.Wait() exactly once and sends
	// to waitCh (buffered). The startup select may have already drained
	// waitCh (process exited early and we returned an error above). If we
	// reached this point, the process is alive and waitCh hasn't been read.
	done := make(chan struct{})
	go func() {
		<-waitCh
		close(done)
	}()

	return &WorkerHandle{
		Worker: worker,
		Kill: func() {
			cmd.Process.Kill()
		},
		Close: func() {
			conn.Close()
			os.Remove(socketPath)
		},
		Done: done,
	}, nil
}
