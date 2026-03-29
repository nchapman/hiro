//go:build isolation

package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/uidpool"
)

// isolationWorker extends testWorker to verify it runs as the expected UID.
type isolationWorker struct {
	testWorker
	spawnCfg ipc.SpawnConfig
}

// isolationWorkerFactory captures SpawnConfig for inspection.
func isolationWorkerFactory(response string) (WorkerFactory, *[]*isolationWorker) {
	var workers []*isolationWorker
	factory := func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
		done := make(chan struct{})
		w := &isolationWorker{
			testWorker: testWorker{response: response, done: done},
			spawnCfg:   cfg,
		}
		workers = append(workers, w)
		return &WorkerHandle{
			Worker: w,
			Kill:   func() { w.closeDone() },
			Close:  func() {},
			Done:   done,
		}, nil
	}
	return factory, &workers
}

func setupIsolationManager(t *testing.T) (*Manager, string, *[]*isolationWorker) {
	t.Helper()

	grp, err := user.LookupGroup("hive-agents")
	if err != nil {
		t.Skip("hive-agents group not found — run in Docker with user pool")
	}
	gid, _ := strconv.ParseUint(grp.Gid, 10, 32)

	dir := t.TempDir()
	// Make the workspace accessible to agent users
	os.Chmod(dir, 0775)
	os.Chown(dir, 0, int(gid))

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	pool := uidpool.New(uidpool.DefaultBaseUID, uint32(gid), uidpool.DefaultSize)
	factory, workers := isolationWorkerFactory("hello")

	mgr := NewManager(t.Context(), dir, Options{
		WorkingDir: dir,
	}, nil, logger, factory, pool, nil)

	return mgr, dir, workers
}

func TestIsolation_UIDAssigned(t *testing.T) {
	mgr, dir, workers := setupIsolationManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if len(*workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(*workers))
	}
	w := (*workers)[0]
	if w.spawnCfg.UID == 0 {
		t.Fatal("expected non-zero UID in SpawnConfig")
	}
	if w.spawnCfg.GID == 0 {
		t.Fatal("expected non-zero GID in SpawnConfig")
	}
	if w.spawnCfg.UID < uidpool.DefaultBaseUID {
		t.Fatalf("UID %d below base %d", w.spawnCfg.UID, uidpool.DefaultBaseUID)
	}
}

func TestIsolation_SessionDirOwnership(t *testing.T) {
	mgr, dir, workers := setupIsolationManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	w := (*workers)[0]
	sessDir := filepath.Join(dir, "instances", id)

	// Verify the session dir is owned by the agent's UID
	info, err := os.Stat(sessDir)
	if err != nil {
		t.Fatalf("stat session dir: %v", err)
	}
	_ = info // ownership check below uses Lstat + syscall

	// Check actual ownership via syscall
	checkOwnership(t, sessDir, w.spawnCfg.UID, w.spawnCfg.GID)
}

func TestIsolation_DifferentUIDs(t *testing.T) {
	mgr, dir, workers := setupIsolationManager(t)
	writeAgentMD(t, dir, "agent-a", testAgentMD)
	writeAgentMD(t, dir, "agent-b", testAgentMD)

	_, err := mgr.CreateInstance(t.Context(), "agent-a", "", "persistent")
	if err != nil {
		t.Fatalf("start agent-a: %v", err)
	}
	_, err = mgr.CreateInstance(t.Context(), "agent-b", "", "persistent")
	if err != nil {
		t.Fatalf("start agent-b: %v", err)
	}

	if len(*workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(*workers))
	}

	uid1 := (*workers)[0].spawnCfg.UID
	uid2 := (*workers)[1].spawnCfg.UID
	if uid1 == uid2 {
		t.Fatalf("both agents got same UID %d", uid1)
	}
}

func TestIsolation_UIDReleasedOnStop(t *testing.T) {
	mgr, dir, _ := setupIsolationManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	mgr.StopInstance(id)

	// UID should be released — we should be able to start 64 more agents
	// (pool size) without exhaustion. Just verify one more works.
	_, err = mgr.CreateInstance(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start after stop: %v", err)
	}
}

func TestIsolation_PoolExhaustion(t *testing.T) {
	grp, err := user.LookupGroup("hive-agents")
	if err != nil {
		t.Skip("hive-agents group not found")
	}
	gid, _ := strconv.ParseUint(grp.Gid, 10, 32)

	dir := t.TempDir()
	os.Chmod(dir, 0775)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	// Create a tiny pool (size 2) to test exhaustion
	pool := uidpool.New(uidpool.DefaultBaseUID, uint32(gid), 2)
	factory, _ := isolationWorkerFactory("hello")

	mgr := NewManager(t.Context(), dir, Options{
		WorkingDir: dir,
	}, nil, logger, factory, pool, nil)

	writeAgentMD(t, dir, "test-agent", testAgentMD)

	ctx := t.Context()
	if _, err := mgr.CreateInstance(ctx, "test-agent", "", "persistent"); err != nil {
		t.Fatalf("start 1: %v", err)
	}
	if _, err := mgr.CreateInstance(ctx, "test-agent", "", "persistent"); err != nil {
		t.Fatalf("start 2: %v", err)
	}

	// Third should fail — pool exhausted
	_, err = mgr.CreateInstance(ctx, "test-agent", "", "persistent")
	if err == nil {
		t.Fatal("expected error on pool exhaustion")
	}
}

func TestIsolation_SessionDirInaccessible(t *testing.T) {
	mgr, dir, workers := setupIsolationManager(t)
	writeAgentMD(t, dir, "agent-a", testAgentMD)
	writeAgentMD(t, dir, "agent-b", testAgentMD)

	idA, _ := mgr.CreateInstance(t.Context(), "agent-a", "", "persistent")
	idB, _ := mgr.CreateInstance(t.Context(), "agent-b", "", "persistent")

	uidA := (*workers)[0].spawnCfg.UID
	uidB := (*workers)[1].spawnCfg.UID

	sessDirA := filepath.Join(dir, "instances", idA)
	sessDirB := filepath.Join(dir, "instances", idB)

	// Verify each session dir is owned by its agent
	checkOwnership(t, sessDirA, uidA, (*workers)[0].spawnCfg.GID)
	checkOwnership(t, sessDirB, uidB, (*workers)[1].spawnCfg.GID)

	// Session dirs have 0700 — agent B's UID shouldn't be able to read agent A's dir
	info, err := os.Stat(sessDirA)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm&0070 != 0 { // no group permissions
		t.Fatalf("session dir has group permissions: %o", perm)
	}
	if perm&0007 != 0 { // no other permissions
		t.Fatalf("session dir has other permissions: %o", perm)
	}
}

// checkOwnership verifies a path is owned by the expected UID/GID.
func checkOwnership(t *testing.T, path string, expectedUID, expectedGID uint32) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}

	stat, ok := fileUID(info)
	if !ok {
		t.Skip("cannot determine file ownership on this platform")
	}

	if stat.uid != expectedUID {
		t.Errorf("%s: expected UID %d, got %d", path, expectedUID, stat.uid)
	}
	if stat.gid != expectedGID {
		t.Errorf("%s: expected GID %d, got %d", path, expectedGID, stat.gid)
	}
}

type fileOwnership struct {
	uid, gid uint32
}

func fileUID(info os.FileInfo) (fileOwnership, bool) {
	// Use fmt to avoid platform-specific imports in this file.
	// The actual syscall extraction is in the platform-specific file.
	return fileUIDPlatform(info)
}

// TestIsolation_ConfigYAMLProtected verifies that config.yaml owned by root
// with mode 0600 is not readable by agent users. The test creates the file as
// root, then attempts to read it as an agent UID using a subprocess.
func TestIsolation_ConfigYAMLProtected(t *testing.T) {
	grp, err := user.LookupGroup("hive-agents")
	if err != nil {
		t.Skip("hive-agents group not found")
	}
	gid, _ := strconv.ParseUint(grp.Gid, 10, 32)

	if os.Getuid() != 0 {
		t.Skip("must run as root")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Create config.yaml as root with 0600
	if err := os.WriteFile(configPath, []byte("secrets:\n  TOKEN: secret123\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Attempt to read as an agent user via subprocess
	cmd := exec.Command("cat", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    uidpool.DefaultBaseUID,
			Gid:    uint32(gid),
			Groups: []uint32{uint32(gid)},
		},
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("agent user should not be able to read config.yaml, got: %s", output)
	}
	// Expect permission denied
	if !strings.Contains(string(output), "Permission denied") {
		t.Fatalf("expected 'Permission denied', got: %s (err: %v)", output, err)
	}
}
