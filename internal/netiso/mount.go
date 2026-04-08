//go:build linux

package netiso

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// setupMounts creates per-agent /etc/resolv.conf and /etc/hosts files
// and bind-mounts them into the agent's mount namespace using nsenter.
func setupMounts(agent AgentNetwork) error {
	gwIP := agent.GatewayIP().String()

	// Write per-agent files to a temp directory.
	tmpDir, err := os.MkdirTemp("", "hiro-netiso-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	// Don't defer RemoveAll — the bind mounts reference these files.
	// They'll be cleaned up when the namespace (and process) exits.

	resolvConf := fmt.Sprintf("nameserver %s\nsearch .\noptions ndots:1\n", gwIP)
	resolvPath := filepath.Join(tmpDir, "resolv.conf")
	if err := os.WriteFile(resolvPath, []byte(resolvConf), 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("writing resolv.conf: %w", err)
	}

	hostsContent := "127.0.0.1 localhost\n::1       localhost\n"
	hostsPath := filepath.Join(tmpDir, "hosts")
	if err := os.WriteFile(hostsPath, []byte(hostsContent), 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("writing hosts: %w", err)
	}

	// Use nsenter to enter the agent's mount namespace and perform bind mounts.
	// This avoids the Go runtime limitation where setns(CLONE_NEWNS) requires
	// a single-threaded process (Go's goroutine scheduler uses multiple threads).
	pid := fmt.Sprintf("%d", agent.PID)
	for _, mount := range []struct{ src, dst string }{
		{resolvPath, "/etc/resolv.conf"},
		{hostsPath, "/etc/hosts"},
	} {
		cmd := exec.Command("nsenter", "--mount", "--target", pid,
			"mount", "--bind", mount.src, mount.dst)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(tmpDir)
			return fmt.Errorf("bind-mounting %s: %w: %s", mount.dst, err, out)
		}
	}

	return nil
}
