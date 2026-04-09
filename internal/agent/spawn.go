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

// configureIsolation sets up UID isolation on the command. All UID-isolated
// workers spawn in their own user, network, and mount namespaces
// (CLONE_NEWUSER | CLONE_NEWNET | CLONE_NEWNS) with UID/GID mappings.
// This provides network isolation (default-deny) and seccomp enforcement
// for every agent.
func configureIsolation(cmd *exec.Cmd, cfg ipc.SpawnConfig) {
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	// CLONE_NEWUSER: UID drop happens via namespace mapping (UID 0 inside
	// maps to agent UID outside). Credential must NOT be set — the child
	// is UID 0 inside its userns and setuid(agentUID) would fail.
	setNetworkCloneflags(cmd, cfg.UID, cfg.GID, cfg.Groups)
	cmd.Env = buildIsolatedEnv(cfg, os.Getenv)
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

// agentProcess holds the pieces returned by startAgentProcess for the
// two-phase handshake protocol.
type agentProcess struct {
	cmd        *exec.Cmd
	scanner    *bufio.Scanner
	stderrBuf  *bytes.Buffer
	waitCh     <-chan error
	vethReadyW *os.File // parent→child pipe; close to signal veth ready (nil if no netiso)
}

// setupVethPipe creates a pipe for the veth-ready signal if network isolation
// is active. The read end goes to the child as FD 3 via ExtraFiles. Returns
// the write end (parent closes it to signal the child), or nil if no pipe needed.
// PeerName is only set when the parent will perform the veth handshake.
//
// The caller must close the write end when done. The read end is owned by the
// child process after cmd.Start(); if startup fails before Start(), the caller
// must call cleanupVethPipe to avoid leaking the read end.
func setupVethPipe(cmd *exec.Cmd, cfg ipc.SpawnConfig) (*os.File, error) {
	if cfg.PeerName == "" {
		return nil, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating veth-ready pipe: %w", err)
	}
	cmd.ExtraFiles = []*os.File{r} // child FD 3
	return w, nil
}

// cleanupVethPipe closes both ends of the veth-ready pipe. Call on error paths
// before cmd.Start() to avoid leaking the read end stored in cmd.ExtraFiles.
func cleanupVethPipe(cmd *exec.Cmd, w *os.File) {
	closeIfNotNil(w)
	for _, f := range cmd.ExtraFiles {
		closeIfNotNil(f)
	}
	cmd.ExtraFiles = nil
}

// closeIfNotNil closes the file if it's not nil.
func closeIfNotNil(f *os.File) {
	if f != nil {
		f.Close()
	}
}

// startAgentProcess prepares and starts an agent process, writes its config,
// and returns the pieces needed for the handshake protocol. Does NOT wait for
// readiness — the caller handles the ns-ready/ready handshake.
//
// If network isolation is active, an extra pipe is created (vethReadyW) whose
// read end is passed to the child as FD 3. The parent closes vethReadyW after
// completing host-side network setup to signal the child to self-configure.
func startAgentProcess(ctx context.Context, cfg ipc.SpawnConfig) (*agentProcess, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving executable: %w", err)
	}

	cmd := exec.CommandContext(ctx, self, "agent") //nolint:gosec // self is our own binary path from os.Executable
	if cfg.UID != 0 {
		configureIsolation(cmd, cfg)
	}

	vethReadyW, err := setupVethPipe(cmd, cfg)
	if err != nil {
		return nil, err
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cleanupVethPipe(cmd, vethReadyW)
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cleanupVethPipe(cmd, vethReadyW)
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		cleanupVethPipe(cmd, vethReadyW)
		return nil, fmt.Errorf("starting agent process: %w", err)
	}

	// Close the pipe read end in the parent — the child inherited it.
	if len(cmd.ExtraFiles) > 0 {
		cmd.ExtraFiles[0].Close()
	}

	if err := json.NewEncoder(stdinPipe).Encode(cfg); err != nil {
		_ = cmd.Process.Kill()
		closeIfNotNil(vethReadyW)
		return nil, fmt.Errorf("writing spawn config: %w", err)
	}
	stdinPipe.Close()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	return &agentProcess{
		cmd:        cmd,
		scanner:    bufio.NewScanner(stdoutPipe),
		stderrBuf:  &stderrBuf,
		waitCh:     waitCh,
		vethReadyW: vethReadyW,
	}, nil
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
//
// When network isolation is active, the spawn uses a two-phase handshake:
//  1. Child forks in CLONE_NEWUSER | CLONE_NEWNET | CLONE_NEWNS, reads config
//  2. Child writes "ns-ready" (namespaces up, awaiting veth)
//  3. Parent does host-side setup: veth pair, nftables, DNS
//  4. Parent closes veth-ready pipe (signals child)
//  5. Child self-configures: rename eth0, IP, routes, bind mounts, seccomp
//  6. Child writes "ready" (gRPC server listening)
//
// Without network isolation, the child writes "ready" directly after startup.
func spawnWorkerProcess(ctx context.Context, cfg ipc.SpawnConfig, ni *netiso.NetIso) (*WorkerHandle, error) { //nolint:funlen // network isolation handshake adds complexity to the core spawn path
	socketPath, socketDir, err := prepareSocketDir(cfg)
	if err != nil {
		return nil, err
	}
	cfg.AgentSocket = socketPath

	needsNetIso := ni != nil && cfg.UID != 0

	// Populate network self-configuration fields before starting the child,
	// so they're available in the SpawnConfig written to stdin.
	if needsNetIso {
		agent := netiso.AgentNetwork{
			AgentID:   cfg.UID - uidpool.DefaultBaseUID,
			SessionID: cfg.SessionID,
		}
		cfg.AgentIP = agent.AgentIP().String()
		cfg.GatewayIP = agent.GatewayIP().String()
		cfg.SubnetBits = 30
		cfg.PeerName = netiso.PeerName(agent.SessionPrefix())
	}

	proc, err := startAgentProcess(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if needsNetIso {
		// Phase 1: Wait for child to signal namespaces are up.
		if err := waitForLine("ns-ready", proc.cmd, proc.scanner, proc.stderrBuf, proc.waitCh); err != nil {
			if proc.vethReadyW != nil {
				proc.vethReadyW.Close()
			}
			return nil, err
		}

		// Phase 2: Host-side network setup (veth, nftables, DNS).
		if setupErr := ni.Setup(ctx, netiso.AgentNetwork{
			AgentID:   cfg.UID - uidpool.DefaultBaseUID,
			SessionID: cfg.SessionID,
			PID:       proc.cmd.Process.Pid,
			Egress:    cfg.NetworkEgress,
		}); setupErr != nil {
			closeIfNotNil(proc.vethReadyW)
			_ = proc.cmd.Process.Kill()
			<-proc.waitCh
			return nil, fmt.Errorf("setting up network isolation: %w", setupErr)
		}

		// Phase 3: Signal child to self-configure.
		// The child's SpawnConfig already has PeerName, AgentIP, GatewayIP,
		// SubnetBits populated. Closing this pipe unblocks the child's read.
		proc.vethReadyW.Close()
	}

	// Wait for the final "ready" signal (gRPC server listening).
	if err := waitForLine("ready", proc.cmd, proc.scanner, proc.stderrBuf, proc.waitCh); err != nil {
		if needsNetIso {
			_ = ni.Teardown(cfg.SessionID)
		}
		return nil, err
	}

	// Connect gRPC client to the agent's worker socket.
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		if needsNetIso {
			_ = ni.Teardown(cfg.SessionID)
		}
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
			if needsNetIso {
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
