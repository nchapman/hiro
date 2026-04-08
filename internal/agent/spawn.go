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
	"syscall"
	"time"

	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/ipc/grpcipc"
	"github.com/nchapman/hiro/internal/netiso"
	"github.com/nchapman/hiro/internal/platform/fsperm"
	"github.com/nchapman/hiro/internal/uidpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const spawnTimeout = 30 * time.Second

// prepareSocketDir creates a private socket directory for the agent process.
// Returns the socket path.
func prepareSocketDir(cfg ipc.SpawnConfig) (socketPath string, socketDir string, err error) {
	sessPrefix := cfg.SessionID
	if len(sessPrefix) > ipc.MaxSessionPrefix {
		sessPrefix = sessPrefix[:ipc.MaxSessionPrefix]
	}
	socketDir = filepath.Join(os.TempDir(), fmt.Sprintf("hiro-%s", sessPrefix))
	if err := os.MkdirAll(socketDir, fsperm.DirPrivate); err != nil {
		return "", "", fmt.Errorf("creating socket dir: %w", err)
	}
	if cfg.UID != 0 {
		if err := os.Chown(socketDir, int(cfg.UID), int(cfg.GID)); err != nil {
			return "", "", fmt.Errorf("chowning socket dir: %w", err)
		}
	}
	return socketDir + "/a.sock", socketDir, nil
}

// configureIsolation sets up UID isolation credentials and environment on the command.
// When network egress policy is set, the worker is also placed in its own
// network and mount namespaces (CLONE_NEWNET + CLONE_NEWNS) on Linux.
func configureIsolation(cmd *exec.Cmd, cfg ipc.SpawnConfig) {
	groups := cfg.Groups
	if len(groups) == 0 {
		groups = []uint32{cfg.GID}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    cfg.UID,
			Gid:    cfg.GID,
			Groups: groups,
		},
	}
	if cfg.NetworkEgress != nil {
		setNetworkCloneflags(cmd)
	}
	cmd.Env = buildIsolatedEnv(cfg, os.Getenv)
}

// waitForReady waits for the agent process to signal readiness on stdout,
// or returns an error if the process exits early or times out.
// On success, callers must consume waitCh to reap the process.
func waitForReady(cmd *exec.Cmd, stdoutPipe *bufio.Scanner, stderrBuf *bytes.Buffer, waitCh <-chan error) error {
	type readyResult struct{ err error }
	readyCh := make(chan readyResult, 1)
	go func() {
		if stdoutPipe.Scan() {
			readyCh <- readyResult{}
		} else {
			readyCh <- readyResult{err: fmt.Errorf("agent process closed stdout without signaling ready")}
		}
	}()

	select {
	case r := <-readyCh:
		if r.err != nil {
			_ = cmd.Process.Kill()
			<-waitCh
			return fmt.Errorf("%w; stderr: %s", r.err, stderrBuf.String())
		}
		return nil
	case waitErr := <-waitCh:
		return fmt.Errorf("agent process exited during startup: %w; stderr: %s", waitErr, stderrBuf.String())
	case <-time.After(spawnTimeout):
		_ = cmd.Process.Kill()
		<-waitCh
		return fmt.Errorf("agent process startup timed out after %s; stderr: %s", spawnTimeout, stderrBuf.String())
	}
}

// startAgentProcess prepares, starts, and writes config to an agent process.
// Returns the command, stderr buffer, and wait channel. The process is running
// and has received its config, but readiness has not been confirmed.
func startAgentProcess(ctx context.Context, cfg ipc.SpawnConfig) (*exec.Cmd, *bytes.Buffer, <-chan error, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving executable: %w", err)
	}

	cmd := exec.CommandContext(ctx, self, "agent") //nolint:gosec // self is our own binary path from os.Executable
	if cfg.UID != 0 {
		configureIsolation(cmd, cfg)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("starting agent process: %w", err)
	}

	if err := json.NewEncoder(stdinPipe).Encode(cfg); err != nil {
		_ = cmd.Process.Kill()
		return nil, nil, nil, fmt.Errorf("writing spawn config: %w", err)
	}
	stdinPipe.Close()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	if err := waitForReady(cmd, bufio.NewScanner(stdoutPipe), &stderrBuf, waitCh); err != nil {
		return nil, nil, nil, err
	}

	return cmd, &stderrBuf, waitCh, nil
}

// newWorkerFactory creates a WorkerFactory that spawns agent processes with
// optional network isolation. If ni is nil, no network isolation is applied.
func newWorkerFactory(ni *netiso.NetIso) WorkerFactory {
	return func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
		return spawnWorkerProcess(ctx, cfg, ni)
	}
}

// defaultWorkerFactory spawns an agent as an OS process using the same binary,
// without network isolation. Used when NetIso is not available.
func defaultWorkerFactory(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
	return spawnWorkerProcess(ctx, cfg, nil)
}

// spawnWorkerProcess is the core spawn logic shared by all worker factories.
// The agent process reads a SpawnConfig from stdin, starts a gRPC AgentWorker
// server on a Unix socket, and writes "ready" to stdout when it's listening.
func spawnWorkerProcess(ctx context.Context, cfg ipc.SpawnConfig, ni *netiso.NetIso) (*WorkerHandle, error) { //nolint:funlen // network isolation adds 15 lines to the core spawn path
	// Create a private socket directory so the socket is inaccessible to other
	// agent UIDs from the moment it's created (no TOCTOU window). Short path
	// to stay under the 104-byte Unix socket limit.
	socketPath, socketDir, err := prepareSocketDir(cfg)
	if err != nil {
		return nil, err
	}
	cfg.AgentSocket = socketPath

	cmd, _, waitCh, err := startAgentProcess(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Between cmd.Start() and gRPC connect, the worker is alive but has zero
	// network if CLONE_NEWNET was set. Set up veth + nftables now.
	if ni != nil && cfg.NetworkEgress != nil && cfg.UID != 0 {
		if err := ni.Setup(ctx, netiso.AgentNetwork{
			AgentID:   cfg.UID - uidpool.DefaultBaseUID,
			SessionID: cfg.SessionID,
			PID:       cmd.Process.Pid,
			Egress:    cfg.NetworkEgress,
		}); err != nil {
			_ = cmd.Process.Kill()
			<-waitCh
			return nil, fmt.Errorf("setting up network isolation: %w", err)
		}
	}

	// Connect gRPC client to the agent's worker socket.
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		if ni != nil && cfg.NetworkEgress != nil {
			_ = ni.Teardown(cfg.SessionID)
		}
		_ = cmd.Process.Kill()
		<-waitCh
		return nil, fmt.Errorf("connecting to agent worker: %w", err)
	}

	worker := grpcipc.NewWorkerClient(conn)

	// Death-watcher: closes done when the process exits.
	done := make(chan struct{})
	go func() {
		<-waitCh
		close(done)
	}()

	return &WorkerHandle{
		Worker: worker,
		Kill: func() {
			_ = cmd.Process.Kill()
		},
		Close: func() {
			conn.Close()
			if ni != nil && cfg.NetworkEgress != nil {
				_ = ni.Teardown(cfg.SessionID)
			}
			_ = os.Remove(socketPath) // best-effort cleanup
			_ = os.Remove(socketDir)  // best-effort cleanup
		},
		Done: done,
	}, nil
}

// forwardedEnvKeys are environment variables forwarded from the control plane
// to isolated agent processes. These configure shared tool managers (mise)
// so agents can find and install tools despite having a minimal environment.
// Note: MISE_INSTALL_PATH is intentionally excluded — it's only needed at
// mise install time, not runtime. The binary is at /usr/local/bin/mise.
var forwardedEnvKeys = []string{
	"MISE_DATA_DIR",
	"MISE_CONFIG_DIR",
	"MISE_CACHE_DIR",
	"MISE_GLOBAL_CONFIG_FILE",
}

// buildIsolatedEnv constructs a minimal environment for an agent process
// running under UID isolation. It includes only what's necessary for the
// agent to function — locale, API key, home directory, PATH, and tool
// manager config — preventing control plane state from leaking.
func buildIsolatedEnv(cfg ipc.SpawnConfig, getenv func(string) string) []string {
	env := []string{
		"PATH=" + getenv("PATH"),
		"HOME=" + cfg.SessionDir,
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
