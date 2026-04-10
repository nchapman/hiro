package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/ipc/grpcipc"
	"github.com/nchapman/hiro/internal/platform/fsperm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const spawnTimeout = 30 * time.Second

// prepareSocketDir creates a private socket directory for the agent process.
// The socket lives under /tmp (not the session dir) to stay within the
// 104-byte Unix socket path length limit. The socket directory is added
// to the Landlock ReadWrite paths so the worker can create the socket file.
func prepareSocketDir(cfg ipc.SpawnConfig) (socketPath string, socketDir string, err error) {
	sessPrefix := cfg.SessionID
	if len(sessPrefix) > ipc.MaxSessionPrefix {
		sessPrefix = sessPrefix[:ipc.MaxSessionPrefix]
	}
	socketDir = filepath.Join(os.TempDir(), fmt.Sprintf("hiro-%s", sessPrefix))
	if err := os.MkdirAll(socketDir, fsperm.DirPrivate); err != nil {
		return "", "", fmt.Errorf("creating socket dir: %w", err)
	}
	return socketDir + "/a.sock", socketDir, nil
}

// waitForLine waits for a specific line on the scanner, or returns an error if
// the process exits early, times out, or sends an unexpected line.
func waitForLine(expected string, cmd *exec.Cmd, scanner *bufio.Scanner, stderrBuf *bytes.Buffer, waitCh <-chan error) error {
	type readyResult struct {
		line string
		err  error
	}
	readyCh := make(chan readyResult, 1)
	go func() {
		if scanner.Scan() {
			readyCh <- readyResult{line: scanner.Text()}
		} else {
			readyCh <- readyResult{err: fmt.Errorf("agent process closed stdout without signaling %s", expected)}
		}
	}()

	select {
	case r := <-readyCh:
		if r.err != nil {
			_ = cmd.Process.Kill()
			<-waitCh
			return fmt.Errorf("%w; stderr: %s", r.err, stderrBuf.String())
		}
		if r.line != expected {
			_ = cmd.Process.Kill()
			<-waitCh
			return fmt.Errorf("expected %q, got %q; stderr: %s", expected, r.line, stderrBuf.String())
		}
		return nil
	case waitErr := <-waitCh:
		return fmt.Errorf("agent process exited during startup (waiting for %s): %w; stderr: %s", expected, waitErr, stderrBuf.String())
	case <-time.After(spawnTimeout):
		_ = cmd.Process.Kill()
		<-waitCh
		return fmt.Errorf("agent process startup timed out after %s (waiting for %s); stderr: %s", spawnTimeout, expected, stderrBuf.String())
	}
}

// startAgentProcess prepares and starts an agent process, writes its config,
// and returns the pieces needed for the ready handshake.
func startAgentProcess(ctx context.Context, cfg ipc.SpawnConfig) (*agentProcess, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving executable: %w", err)
	}

	cmd := exec.CommandContext(ctx, self, "agent") //nolint:gosec // self is our own binary path from os.Executable
	cmd.Env = buildIsolatedEnv(cfg, os.Getenv)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent process: %w", err)
	}

	if err := json.NewEncoder(stdinPipe).Encode(cfg); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("writing spawn config: %w", err)
	}
	stdinPipe.Close()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	return &agentProcess{
		cmd:       cmd,
		scanner:   bufio.NewScanner(stdoutPipe),
		stderrBuf: &stderrBuf,
		waitCh:    waitCh,
	}, nil
}

// agentProcess holds the pieces returned by startAgentProcess.
type agentProcess struct {
	cmd       *exec.Cmd
	scanner   *bufio.Scanner
	stderrBuf *bytes.Buffer
	waitCh    <-chan error
}

// defaultWorkerFactory spawns an agent as an OS process using the same binary.
func defaultWorkerFactory(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
	return spawnWorkerProcess(ctx, cfg)
}

// spawnWorkerProcess is the core spawn logic. Starts the worker process,
// waits for the "ready" signal, and connects via gRPC.
func spawnWorkerProcess(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
	socketPath, socketDir, err := prepareSocketDir(cfg)
	if err != nil {
		return nil, err
	}
	cfg.AgentSocket = socketPath

	proc, err := startAgentProcess(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Wait for the "ready" signal (gRPC server listening).
	if err := waitForLine("ready", proc.cmd, proc.scanner, proc.stderrBuf, proc.waitCh); err != nil {
		return nil, err
	}

	// Connect gRPC client to the agent's worker socket.
	conn, err := grpc.NewClient("unix:"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		_ = proc.cmd.Process.Kill()
		<-proc.waitCh
		return nil, fmt.Errorf("connecting to agent worker: %w", err)
	}

	worker := grpcipc.NewWorkerClient(conn)

	// Death-watcher: closes done when the process exits.
	done := make(chan struct{})
	go func() {
		<-proc.waitCh
		close(done)
	}()

	return &WorkerHandle{
		Worker: worker,
		Kill: func() {
			_ = proc.cmd.Process.Kill()
		},
		Close: func() {
			conn.Close()
			_ = os.Remove(socketPath) // best-effort cleanup
			_ = os.Remove(socketDir)  // best-effort cleanup
		},
		Done: done,
	}, nil
}

// forwardedEnvKeys are environment variables forwarded from the control plane
// to isolated agent processes. These configure shared tool managers (mise)
// so agents can find and install tools despite having a minimal environment.
var forwardedEnvKeys = []string{
	"MISE_DATA_DIR",
	"MISE_CONFIG_DIR",
	"MISE_CACHE_DIR",
	"MISE_GLOBAL_CONFIG_FILE",
}

// buildIsolatedEnv constructs a minimal environment for an agent process.
// It includes only what's necessary for the agent to function — locale,
// home directory, PATH, and tool manager config — preventing control
// plane state from leaking.
func buildIsolatedEnv(cfg ipc.SpawnConfig, getenv func(string) string) []string {
	tmpDir := filepath.Join(cfg.SessionDir, "tmp")
	env := []string{
		"PATH=" + getenv("PATH"),
		"HOME=" + cfg.SessionDir,
		"TMPDIR=" + tmpDir, // direct temp files into the writable session dir
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		// Workers don't need the API key — inference runs in the control plane.
	}
	for _, key := range forwardedEnvKeys {
		if v := getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}
