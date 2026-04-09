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

// TestIsolation_MiseDataDirReadOnly verifies that agent users can read
// but NOT write to the shared mise data directory. Root owns everything —
// agents cannot inject malicious shims or replace binaries.
func TestIsolation_MiseDataDirReadOnly(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	miseDir := os.Getenv("MISE_DATA_DIR")
	if miseDir == "" {
		t.Skip("MISE_DATA_DIR not set")
	}

	// Agent should NOT be able to write to the shared mise directory.
	testFile := miseDir + "/test-write-permission"
	cmd := agentCmd(t, uid, gid, sessDir, "touch", testFile)
	if err := cmd.Run(); err == nil {
		os.Remove(testFile)
		t.Fatal("agent user should not be able to write to MISE_DATA_DIR")
	}

	// Agent should be able to read mise binaries.
	cmd = agentCmd(t, uid, gid, sessDir, "ls", miseDir)
	if err := cmd.Run(); err != nil {
		t.Fatalf("agent user cannot read MISE_DATA_DIR: %v", err)
	}
}

// TestIsolation_MiseGlobalReadOnly verifies that agent users cannot install
// tools globally (shared /opt/mise is root-owned and read-only to agents).
func TestIsolation_MiseGlobalReadOnly(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	if os.Getenv("MISE_DATA_DIR") == "" {
		t.Skip("MISE_DATA_DIR not set")
	}

	// "mise use -g" should fail because agents can't write to /opt/mise.
	cmd := agentCmd(t, uid, gid, sessDir, "sh", "-c",
		"mise use -g jq@latest 2>&1")
	cmd.Dir = sessDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("agent should not be able to install tools globally, but succeeded: %s", output)
	}
	t.Logf("global install correctly denied: %s", strings.TrimSpace(string(output)))
}

// TestIsolation_MisePreinstalledToolsWork verifies that pre-installed mise
// tools (node, python) are usable by agent users via shims, even though
// agents can't install new tools globally.
func TestIsolation_MisePreinstalledToolsWork(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	if os.Getenv("MISE_DATA_DIR") == "" {
		t.Skip("MISE_DATA_DIR not set")
	}

	for _, tool := range []struct{ name, check string }{
		{"node", "node --version"},
		{"python", "python3 --version"},
	} {
		t.Run(tool.name, func(t *testing.T) {
			cmd := agentCmd(t, uid, gid, sessDir, "sh", "-c", tool.check)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("pre-installed %s not usable by agent: %v\n%s", tool.name, err, output)
			}
			t.Logf("%s: %s", tool.name, strings.TrimSpace(string(output)))
		})
	}
}

// TestIsolation_SupplementaryGroupAccess verifies that an agent with the
// hiro-operators supplementary group can write to a setgid directory owned
// by root:hiro-operators. This tests the Credential-based path used by
// these isolation tests (the production path uses CLONE_NEWUSER + GidMappings
// + setgroups(), which is tested by make test-netiso).
func TestIsolation_SupplementaryGroupAccess(t *testing.T) {
	uid, gid := requireIsolation(t)
	sessDir := agentSessionDir(t, uid, gid)

	opGrp, err := user.LookupGroup("hiro-operators")
	if err != nil {
		t.Skip("hiro-operators group not found")
	}
	opGID, err := strconv.ParseUint(opGrp.Gid, 10, 32)
	if err != nil {
		t.Fatalf("parsing hiro-operators GID %q: %v", opGrp.Gid, err)
	}

	// Create a directory mimicking agents/ — root:hiro-operators with setgid.
	targetDir, err := os.MkdirTemp("/tmp", "hiro-operators-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(targetDir) })
	if err := os.Chown(targetDir, 0, int(opGID)); err != nil {
		t.Fatalf("chown: %v", err)
	}
	if err := os.Chmod(targetDir, 0o2775); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	// Agent WITHOUT hiro-operators should NOT be able to write.
	cmdNoGroup := agentCmd(t, uid, gid, sessDir, "touch", targetDir+"/nope")
	if err := cmdNoGroup.Run(); err == nil {
		t.Fatal("agent without hiro-operators should not write to operators dir")
	}

	// Agent WITH hiro-operators supplementary group should be able to write.
	cmdWithGroup := exec.Command("touch", targetDir+"/yes")
	cmdWithGroup.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    uid,
			Gid:    gid,
			Groups: []uint32{gid, uint32(opGID)},
		},
	}
	cmdWithGroup.Env = buildIsolatedEnv(ipcSpawnConfig(sessDir), os.Getenv)
	if err := cmdWithGroup.Run(); err != nil {
		t.Fatalf("agent with hiro-operators should write to operators dir: %v", err)
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
