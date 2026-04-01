//go:build isolation

package agent

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/uidpool"
)

// agentCmd creates an exec.Cmd that runs as an agent user with the
// environment that buildIsolatedEnv would produce. This mirrors what
// defaultWorkerFactory does for real agent processes.
func agentCmd(t *testing.T, uid, gid uint32, sessionDir string, command string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    uid,
			Gid:    gid,
			Groups: []uint32{gid},
		},
	}
	cmd.Env = buildIsolatedEnv(
		ipcSpawnConfig(sessionDir),
		os.Getenv,
	)
	return cmd
}

// ipcSpawnConfig creates a minimal SpawnConfig for env building.
func ipcSpawnConfig(sessionDir string) ipc.SpawnConfig {
	return ipc.SpawnConfig{
		SessionDir: sessionDir,
		APIKey:     "test-key",
		UID:        uidpool.DefaultBaseUID,
	}
}

func requireIsolation(t *testing.T) (uid, gid uint32) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("must run as root")
	}
	grp, err := user.LookupGroup("hiro-agents")
	if err != nil {
		t.Skip("hiro-agents group not found")
	}
	g, _ := strconv.ParseUint(grp.Gid, 10, 32)
	return uidpool.DefaultBaseUID, uint32(g)
}

// agentSessionDir creates a temp directory owned by the agent user, suitable
// for use as HOME. Unlike t.TempDir(), the entire path is accessible to the
// agent UID (t.TempDir creates root-owned parent dirs that block traversal).
func agentSessionDir(t *testing.T, uid, gid uint32) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hiro-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chown(dir, int(uid), int(gid)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestIsolation_MiseOnPath verifies that the mise binary is accessible
// to agent users via PATH.
func TestIsolation_MiseOnPath(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	cmd := agentCmd(t, uid, gid, sessDir, "which", "mise")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mise not found on PATH: %v\n%s", err, output)
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		t.Fatal("which mise returned empty path")
	}
	t.Logf("mise found at: %s", path)
}

// TestIsolation_MiseShimsWork verifies that mise-installed tools (node,
// python) are accessible to agent users via the shims on PATH.
func TestIsolation_MiseShimsWork(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	tests := []struct {
		name    string
		command string
		args    []string
	}{
		{"node", "node", []string{"--version"}},
		{"python", "python3", []string{"--version"}},
		{"npm", "npm", []string{"--version"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := agentCmd(t, uid, gid, sessDir, tt.command, tt.args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s not accessible: %v\n%s", tt.command, err, output)
			}
			version := strings.TrimSpace(string(output))
			if version == "" {
				t.Fatalf("%s returned empty version", tt.command)
			}
			t.Logf("%s: %s", tt.command, version)
		})
	}
}

// TestIsolation_MiseDataDirInEnv verifies that MISE_DATA_DIR is set
// in the agent environment and points to the shared location.
func TestIsolation_MiseDataDirInEnv(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	expected := os.Getenv("MISE_DATA_DIR")
	if expected == "" {
		t.Skip("MISE_DATA_DIR not set in test environment")
	}

	cmd := agentCmd(t, uid, gid, sessDir, "sh", "-c", "echo $MISE_DATA_DIR")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read MISE_DATA_DIR: %v\n%s", err, output)
	}
	got := strings.TrimSpace(string(output))
	if got != expected {
		t.Errorf("MISE_DATA_DIR = %q, want %q", got, expected)
	}
}

// TestIsolation_HomeIsSessionDir verifies that HOME is set to the
// agent's session directory, not /tmp or any shared location.
func TestIsolation_HomeIsSessionDir(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	cmd := agentCmd(t, uid, gid, sessDir, "sh", "-c", "echo $HOME")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read HOME: %v\n%s", err, output)
	}
	got := strings.TrimSpace(string(output))
	if got != sessDir {
		t.Errorf("HOME = %q, want %q", got, sessDir)
	}
}

// TestIsolation_MiseDataDirGroupWritable verifies that agent users can
// write to the mise data directory (needed for runtime tool installs).
func TestIsolation_MiseDataDirGroupWritable(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	miseDir := os.Getenv("MISE_DATA_DIR")
	if miseDir == "" {
		t.Skip("MISE_DATA_DIR not set")
	}

	// Try to create and remove a file in the mise data dir as the agent user.
	testFile := miseDir + "/test-write-permission"
	cmd := agentCmd(t, uid, gid, sessDir, "sh", "-c",
		`touch "$1" && echo ok && rm "$1"`, "--", testFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent user cannot write to MISE_DATA_DIR: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "ok") {
		t.Fatalf("unexpected output: %s", output)
	}
}

// TestIsolation_MiseInstallGlobal verifies that an agent user can install
// a new tool globally via mise and use it immediately through shims.
func TestIsolation_MiseInstallGlobal(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	if os.Getenv("MISE_DATA_DIR") == "" {
		t.Skip("MISE_DATA_DIR not set")
	}

	// "mise use -g" installs and registers globally — shim works immediately.
	// This mutates the shared /opt/mise global config, which is fine because
	// isolation tests run in ephemeral Docker containers.
	cmd := agentCmd(t, uid, gid, sessDir, "sh", "-c",
		"mise use -g jq@latest && jq --version")
	cmd.Dir = sessDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent failed to install tool globally: %v\n%s", err, output)
	}
	t.Logf("output: %s", strings.TrimSpace(string(output)))
}

// TestIsolation_MiseInstallLocal verifies that an agent user can install
// a tool locally in a project directory via mise and use it through shims.
func TestIsolation_MiseInstallLocal(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	if os.Getenv("MISE_DATA_DIR") == "" {
		t.Skip("MISE_DATA_DIR not set")
	}

	// "mise use" (no -g) creates a local mise.toml in the working directory.
	// The shim resolves the tool when run from that directory.
	projectDir := sessDir + "/project"
	cmd := agentCmd(t, uid, gid, sessDir, "sh", "-c",
		"mkdir -p "+projectDir+" && cd "+projectDir+" && mise use jq@latest && jq --version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent failed to install tool locally: %v\n%s", err, output)
	}
	t.Logf("output: %s", strings.TrimSpace(string(output)))

	// Verify local config was created in the project directory.
	cmd2 := agentCmd(t, uid, gid, sessDir, "cat", projectDir+"/mise.toml")
	config, err := cmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("local mise.toml not created: %v\n%s", err, config)
	}
	if !strings.Contains(string(config), "jq") {
		t.Errorf("mise.toml doesn't mention jq: %s", config)
	}
}

// TestIsolation_NoEnvLeak verifies that the isolated environment does
// not contain control plane variables that weren't explicitly forwarded.
func TestIsolation_NoEnvLeak(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	cmd := agentCmd(t, uid, gid, sessDir, "env")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("env command failed: %v\n%s", err, output)
	}

	allowed := map[string]bool{
		"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true,
		"HIRO_API_KEY": true, "MISE_DATA_DIR": true, "MISE_CONFIG_DIR": true,
		"MISE_CACHE_DIR": true, "MISE_GLOBAL_CONFIG_FILE": true,
		// PWD is set by the kernel on execve.
		"PWD": true,
	}

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		if !allowed[key] {
			t.Errorf("unexpected env var leaked: %s", key)
		}
	}
}
