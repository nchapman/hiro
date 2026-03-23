package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/uidpool"
)

// testWorker implements ipc.AgentWorker for testing.
type testWorker struct {
	response string
	shutdown bool
	done     chan struct{}
	closed   bool
}

func (w *testWorker) Chat(_ context.Context, message string, onDelta func(string) error) (string, error) {
	if onDelta != nil {
		onDelta(w.response)
	}
	return w.response, nil
}

func (w *testWorker) Shutdown(_ context.Context) error {
	w.shutdown = true
	w.closeDone()
	return nil
}

func (w *testWorker) closeDone() {
	if !w.closed {
		w.closed = true
		close(w.done)
	}
}

// testWorkerFactory returns a WorkerFactory that creates testWorkers.
// The done channel is closed when Shutdown is called, simulating process exit.
func testWorkerFactory(response string) WorkerFactory {
	return func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
		done := make(chan struct{})
		w := &testWorker{response: response, done: done}
		return &WorkerHandle{
			Worker: w,
			Kill:   func() { w.closeDone() },
			Close:  func() {},
			Done:   done,
		}, nil
	}
}

func setupTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", testWorkerFactory("hello from agent"), nil)
	return mgr, dir
}

// writeAgentMD writes an agent definition into <rootDir>/agents/<name>/agent.md.
func writeAgentMD(t *testing.T, rootDir, name, content string) {
	t.Helper()
	agentDir := filepath.Join(rootDir, "agents", name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const testAgentMD = `---
name: test-agent
model: fake-model
---

You are a test agent.`

func TestManager_NewManager(t *testing.T) {
	mgr, _ := setupTestManager(t)
	agents := mgr.ListAgents()
	if len(agents) != 0 {
		t.Errorf("new manager should have 0 agents, got %d", len(agents))
	}
}

func TestManager_StartAgent(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty agent ID")
	}

	info, ok := mgr.GetAgent(id)
	if !ok {
		t.Fatal("agent not found after start")
	}
	if info.Name != "test-agent" {
		t.Errorf("name = %q, want %q", info.Name, "test-agent")
	}
	if info.Mode != "persistent" {
		t.Errorf("mode = %q, want persistent", info.Mode)
	}
}

func TestManager_StartAgent_MissingConfig(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.StartAgent(t.Context(), "nonexistent", "")
	if err == nil {
		t.Fatal("expected error for missing agent config")
	}
}

func TestManager_SendMessage(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	result, err := mgr.SendMessage(t.Context(), id, "hi", nil)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result != "hello from agent" {
		t.Errorf("result = %q, want %q", result, "hello from agent")
	}
}

func TestManager_SendMessage_WithDelta(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	var deltas []string
	_, err = mgr.SendMessage(t.Context(), id, "hi", func(text string) error {
		deltas = append(deltas, text)
		return nil
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(deltas) == 0 {
		t.Error("expected at least one delta callback")
	}
}

func TestManager_StopAgent(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	info, err := mgr.StopAgent(id)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if info.Name != "test-agent" {
		t.Errorf("stopped name = %q, want test-agent", info.Name)
	}

	_, ok := mgr.GetAgent(id)
	if ok {
		t.Error("agent should not exist after stop")
	}
}

func TestManager_StopAgent_WithChildren(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "parent", `---
name: parent
model: fake-model
---
Parent agent.`)
	writeAgentMD(t, dir, "child", `---
name: child
model: fake-model
---
Child agent.`)

	parentID, _ := mgr.StartAgent(t.Context(), "parent", "")
	childID, _ := mgr.StartAgent(t.Context(), "child", parentID)

	// Stopping parent should also stop child
	mgr.StopAgent(parentID)

	if _, ok := mgr.GetAgent(parentID); ok {
		t.Error("parent should be stopped")
	}
	if _, ok := mgr.GetAgent(childID); ok {
		t.Error("child should be stopped with parent")
	}
}

func TestManager_StopAgent_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.StopAgent("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for stopping nonexistent agent")
	}
}

func TestManager_AgentByName(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.StartAgent(t.Context(), "test-agent", "")

	found, ok := mgr.AgentByName("test-agent")
	if !ok {
		t.Fatal("expected to find agent by name")
	}
	if found != id {
		t.Errorf("found ID = %q, want %q", found, id)
	}
}

func TestManager_AgentByName_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, found := mgr.AgentByName("nope")
	if found {
		t.Error("expected not found")
	}
}

func TestManager_GetAgent_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, found := mgr.GetAgent("nonexistent-id")
	if found {
		t.Error("expected not found")
	}
}

func TestManager_SendMessage_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.SendMessage(t.Context(), "nonexistent-id", "hello", nil)
	if err == nil {
		t.Fatal("expected error for messaging nonexistent agent")
	}
}

func TestManager_ListAgents(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "a1", `---
name: agent-one
model: fake-model
---
Agent one.`)
	writeAgentMD(t, dir, "a2", `---
name: agent-two
model: fake-model
---
Agent two.`)

	mgr.StartAgent(t.Context(), "a1", "")
	mgr.StartAgent(t.Context(), "a2", "")

	agents := mgr.ListAgents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	names := map[string]bool{}
	for _, a := range agents {
		names[a.Name] = true
	}
	if !names["agent-one"] || !names["agent-two"] {
		t.Errorf("expected agent-one and agent-two, got %v", names)
	}
}

func TestManager_Shutdown(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	mgr.StartAgent(t.Context(), "test-agent", "")
	mgr.Shutdown()

	if len(mgr.ListAgents()) != 0 {
		t.Error("expected 0 agents after shutdown")
	}
}

func TestManager_ParentLineage(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "root", `---
name: root
model: fake-model
---
Root.`)
	writeAgentMD(t, dir, "child", `---
name: child
model: fake-model
---
Child.`)

	rootID, _ := mgr.StartAgent(t.Context(), "root", "")
	childID, _ := mgr.StartAgent(t.Context(), "child", rootID)

	info, _ := mgr.GetAgent(childID)
	if info.ParentID != rootID {
		t.Errorf("child parentID = %q, want %q", info.ParentID, rootID)
	}
}

func TestManager_AgentDefDir(t *testing.T) {
	mgr, dir := setupTestManager(t)
	got := mgr.agentDefDir("coordinator")
	want := filepath.Join(dir, "agents", "coordinator")
	if got != want {
		t.Errorf("agentDefDir = %q, want %q", got, want)
	}
}

func TestManager_SharedSkillsDir(t *testing.T) {
	mgr, dir := setupTestManager(t)
	got := mgr.sharedSkillsDir()
	want := filepath.Join(dir, "skills")
	if got != want {
		t.Errorf("sharedSkillsDir = %q, want %q", got, want)
	}
}

func TestValidateAgentName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"coordinator", false},
		{"my-agent", false},
		{"agent_v2", false},
		{"Agent123", false},
		{"", true},
		{"..", true},
		{".", true},
		{"../escape", true},
		{"path/traversal", true},
		{"back\\slash", true},
		{"has space", true},
		{"null\x00byte", true},
		{"special!char", true},
		{"agent.name", true},
	}
	for _, tt := range tests {
		err := validateAgentName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateAgentName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestManager_IsDescendant(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "root", `---
name: root
model: fake-model
---
Root.`)
	writeAgentMD(t, dir, "child", `---
name: child
model: fake-model
---
Child.`)

	rootID, _ := mgr.StartAgent(t.Context(), "root", "")
	childID, _ := mgr.StartAgent(t.Context(), "child", rootID)

	if !mgr.IsDescendant(childID, rootID) {
		t.Error("child should be descendant of root")
	}
	if !mgr.IsDescendant(rootID, rootID) {
		t.Error("root should be descendant of itself")
	}
	if mgr.IsDescendant(rootID, childID) {
		t.Error("root should not be descendant of child")
	}
}

func TestManager_ListChildren(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "parent", `---
name: parent
model: fake-model
---
Parent.`)
	writeAgentMD(t, dir, "child", `---
name: child
model: fake-model
---
Child.`)

	parentID, _ := mgr.StartAgent(t.Context(), "parent", "")
	mgr.StartAgent(t.Context(), "child", parentID)

	// ListChildren scoped to parent
	children := mgr.ListChildren(parentID)
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if children[0].Name != "child" {
		t.Errorf("child name = %q, want child", children[0].Name)
	}

	// ListChildren for agent with no children
	noKids := mgr.ListChildren("nonexistent")
	if len(noKids) != 0 {
		t.Errorf("expected 0 children, got %d", len(noKids))
	}
}

func TestManager_SessionDirCreated(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	manifestPath := filepath.Join(dir, "sessions", id, "manifest.yaml")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest.yaml should exist at %s: %v", manifestPath, err)
	}
}

func TestManager_SessionSubdirs(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	sessDir := filepath.Join(dir, "sessions", id)
	for _, sub := range []string{"db", "scratch", "tmp"} {
		subDir := filepath.Join(sessDir, sub)
		info, err := os.Stat(subDir)
		if err != nil {
			t.Errorf("session subdir %s should exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", sub)
		}
	}
}

func TestManager_EphemeralCleanup(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	// Start a persistent parent
	parentID, _ := mgr.StartAgent(t.Context(), "test-agent", "")

	// Start an ephemeral child directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("test-agent"))
	cfg.Mode = config.ModeEphemeral
	ephID, _ := mgr.startSession(t.Context(), "ephemeral-test-id", cfg, parentID)

	sessDir := filepath.Join(dir, "sessions", ephID)
	if _, err := os.Stat(sessDir); err != nil {
		t.Fatalf("ephemeral session dir should exist: %v", err)
	}

	mgr.StopAgent(ephID)

	if _, err := os.Stat(sessDir); !os.IsNotExist(err) {
		t.Error("ephemeral session dir should be cleaned up after stop")
	}
}

func TestManager_PersistentNotCleaned(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.StartAgent(t.Context(), "test-agent", "")
	sessDir := filepath.Join(dir, "sessions", id)

	mgr.StopAgent(id)

	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		t.Error("persistent session dir should survive stop")
	}
}

func TestManager_RestoreSessions(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	// Create a manager, start an agent, then shut down
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()
	mgr1 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", testWorkerFactory("hello"), nil)

	id, err := mgr1.StartAgent(ctx, "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()

	// Create a new manager and restore
	mgr2 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", testWorkerFactory("hello"), nil)
	if err := mgr2.RestoreSessions(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The agent should be running with the same ID
	info, ok := mgr2.GetAgent(id)
	if !ok {
		t.Fatal("restored agent not found")
	}
	if info.Name != "test-agent" {
		t.Errorf("restored name = %q, want test-agent", info.Name)
	}
}

func TestManager_SpawnSubagent(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	result, err := mgr.SpawnSubagent(t.Context(), "test-agent", "do something", "", nil)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if result != "hello from agent" {
		t.Errorf("result = %q, want %q", result, "hello from agent")
	}

	// Ephemeral agent should be cleaned up
	agents := mgr.ListAgents()
	if len(agents) != 0 {
		t.Errorf("expected 0 agents after spawn, got %d", len(agents))
	}
}

func TestTruncateResult(t *testing.T) {
	short := "hello"
	if got := truncateResult(short); got != short {
		t.Errorf("short string should not be truncated, got %q", got)
	}

	long := strings.Repeat("x", maxAgentResultSize+100)
	got := truncateResult(long)
	if len(got) > maxAgentResultSize+50 {
		t.Errorf("truncated result too long: %d", len(got))
	}
	if !strings.HasSuffix(got, "(result truncated)") {
		t.Error("expected truncation suffix")
	}
}

// --- UID pool integration tests ---

// capturingWorkerFactory creates workers and records the SpawnConfig for each.
func capturingWorkerFactory(response string) (WorkerFactory, *[]ipc.SpawnConfig) {
	var configs []ipc.SpawnConfig
	factory := func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
		configs = append(configs, cfg)
		done := make(chan struct{})
		w := &testWorker{response: response, done: done}
		return &WorkerHandle{
			Worker: w,
			Kill:   func() { w.closeDone() },
			Close:  func() {},
			Done:   done,
		}, nil
	}
	return factory, &configs
}

// failingWorkerFactory returns an error on every spawn attempt.
func failingWorkerFactory() WorkerFactory {
	return func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
		return nil, fmt.Errorf("simulated spawn failure")
	}
}

func setupTestManagerWithPool(t *testing.T, pool *uidpool.Pool) (*Manager, string, *[]ipc.SpawnConfig) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	factory, configs := capturingWorkerFactory("hello")
	mgr := NewManager(t.Context(), dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", factory, pool)
	return mgr, dir, configs
}

func TestManager_UIDPool_Assigned(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if len(*configs) != 1 {
		t.Fatalf("expected 1 spawn config, got %d", len(*configs))
	}
	cfg := (*configs)[0]
	if cfg.UID == 0 {
		t.Fatal("expected non-zero UID in SpawnConfig")
	}
	if cfg.GID == 0 {
		t.Fatal("expected non-zero GID in SpawnConfig")
	}
	if cfg.UID != 10000 {
		t.Errorf("expected UID 10000, got %d", cfg.UID)
	}
	if pool.InUse() != 1 {
		t.Errorf("expected 1 UID in use, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_DifferentUIDs(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "a", `---
name: agent-a
model: fake-model
---
A.`)
	writeAgentMD(t, dir, "b", `---
name: agent-b
model: fake-model
---
B.`)

	mgr.StartAgent(t.Context(), "a", "")
	mgr.StartAgent(t.Context(), "b", "")

	if len(*configs) != 2 {
		t.Fatalf("expected 2 spawn configs, got %d", len(*configs))
	}
	if (*configs)[0].UID == (*configs)[1].UID {
		t.Fatalf("agents should get different UIDs, both got %d", (*configs)[0].UID)
	}
	if pool.InUse() != 2 {
		t.Errorf("expected 2 UIDs in use, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_ReleasedOnStop(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, _ := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.StartAgent(t.Context(), "test-agent", "")
	if pool.InUse() != 1 {
		t.Fatalf("expected 1 UID in use, got %d", pool.InUse())
	}

	mgr.StopAgent(id)
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs in use after stop, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_ReleasedOnShutdown(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, _ := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	mgr.StartAgent(t.Context(), "test-agent", "")
	mgr.StartAgent(t.Context(), "test-agent", "")
	if pool.InUse() != 2 {
		t.Fatalf("expected 2 UIDs in use, got %d", pool.InUse())
	}

	mgr.Shutdown()
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs in use after shutdown, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_ReleasedOnSpawnFailure(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", failingWorkerFactory(), pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err == nil {
		t.Fatal("expected spawn failure")
	}

	// UID should be released despite spawn failure
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs in use after spawn failure, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_Exhaustion(t *testing.T) {
	pool := uidpool.New(10000, 10000, 2) // tiny pool
	mgr, dir, _ := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}
	_, err = mgr.StartAgent(t.Context(), "test-agent", "")
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	_, err = mgr.StartAgent(t.Context(), "test-agent", "")
	if err == nil {
		t.Fatal("expected pool exhaustion error")
	}
}

func TestManager_UIDPool_StopChildReleasesUID(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, _ := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "parent", `---
name: parent
model: fake-model
---
Parent.`)
	writeAgentMD(t, dir, "child", `---
name: child
model: fake-model
---
Child.`)

	parentID, _ := mgr.StartAgent(t.Context(), "parent", "")
	mgr.StartAgent(t.Context(), "child", parentID)
	if pool.InUse() != 2 {
		t.Fatalf("expected 2 UIDs, got %d", pool.InUse())
	}

	// Stop parent — should release both parent and child UIDs
	mgr.StopAgent(parentID)
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs after stopping parent+child, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_RestoreSessions(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Start with a pool, create an agent, shut down
	factory1, _ := capturingWorkerFactory("hello")
	mgr1 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", factory1, pool)

	id, err := mgr1.StartAgent(ctx, "test-agent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 after shutdown, got %d", pool.InUse())
	}

	// Restore with a fresh pool
	pool2 := uidpool.New(10000, 10000, 64)
	factory2, configs2 := capturingWorkerFactory("hello")
	mgr2 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", factory2, pool2)
	if err := mgr2.RestoreSessions(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Restored agent should have a UID assigned
	if pool2.InUse() != 1 {
		t.Fatalf("expected 1 UID in use after restore, got %d", pool2.InUse())
	}
	if len(*configs2) != 1 {
		t.Fatalf("expected 1 spawn config, got %d", len(*configs2))
	}
	if (*configs2)[0].UID == 0 {
		t.Fatal("restored agent should have non-zero UID")
	}

	// Verify same agent ID
	info, ok := mgr2.GetAgent(id)
	if !ok {
		t.Fatal("restored agent not found")
	}
	if info.Name != "test-agent" {
		t.Errorf("restored name = %q, want test-agent", info.Name)
	}
}

func TestManager_UIDPool_SpawnSubagent(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.SpawnSubagent(t.Context(), "test-agent", "do something", "", nil)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Ephemeral agent should have gotten a UID
	if len(*configs) != 1 {
		t.Fatalf("expected 1 spawn config, got %d", len(*configs))
	}
	if (*configs)[0].UID == 0 {
		t.Fatal("ephemeral agent should have non-zero UID")
	}

	// UID should be released after subagent completes
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs after subagent cleanup, got %d", pool.InUse())
	}
}

func TestManager_EffectiveTools(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "tooled", `---
name: tooled
model: fake-model
tools:
  - bash
  - read_file
  - write_file
---
Agent with tools.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("tooled"))
	effective := mgr.computeEffectiveTools(cfg, "")

	if !effective["bash"] || !effective["read_file"] || !effective["write_file"] {
		t.Errorf("effective tools should include all declared tools, got %v", effective)
	}
	if effective["glob"] {
		t.Error("glob should not be in effective tools (not declared)")
	}
}

func TestBuildAllowedToolsMap_EphemeralMode(t *testing.T) {
	effective := map[string]bool{"bash": true, "read_file": true}
	allowed := buildAllowedToolsMap(effective, config.ModeEphemeral, false)

	// Ephemeral agents get spawn_agent but NOT coordinator or persistent tools.
	if !allowed["spawn_agent"] {
		t.Error("ephemeral agents should get spawn_agent")
	}
	if allowed["start_agent"] || allowed["stop_agent"] || allowed["send_message"] || allowed["list_agents"] {
		t.Error("ephemeral agents should not get coordinator tools")
	}
	if allowed["memory_read"] || allowed["memory_write"] || allowed["todos"] {
		t.Error("ephemeral agents should not get persistent tools")
	}
}

func TestBuildAllowedToolsMap_PersistentMode(t *testing.T) {
	effective := map[string]bool{"bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModePersistent, false)

	// Persistent agents get spawn_agent + persistent tools, but NOT coordinator tools.
	if !allowed["spawn_agent"] {
		t.Error("persistent agents should get spawn_agent")
	}
	if !allowed["memory_read"] || !allowed["memory_write"] || !allowed["todos"] ||
		!allowed["history_search"] || !allowed["history_recall"] {
		t.Error("persistent agents should get persistent tools")
	}
	if allowed["start_agent"] || allowed["stop_agent"] || allowed["send_message"] || allowed["list_agents"] {
		t.Error("persistent agents should not get coordinator tools")
	}
}

func TestBuildAllowedToolsMap_CoordinatorMode(t *testing.T) {
	effective := map[string]bool{"bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModeCoordinator, false)

	// Coordinator agents get everything: spawn + coordinator + persistent tools.
	if !allowed["spawn_agent"] {
		t.Error("coordinators should get spawn_agent")
	}
	if !allowed["start_agent"] || !allowed["stop_agent"] || !allowed["send_message"] || !allowed["list_agents"] {
		t.Error("coordinators should get coordinator tools")
	}
	if !allowed["memory_read"] || !allowed["memory_write"] || !allowed["todos"] ||
		!allowed["history_search"] || !allowed["history_recall"] {
		t.Error("coordinators should get persistent tools")
	}
}

func TestBuildAllowedToolsMap_WithSkills(t *testing.T) {
	effective := map[string]bool{"bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModeEphemeral, true)
	if !allowed["use_skill"] {
		t.Error("use_skill should be included when hasSkills is true")
	}
}

func TestManager_CoordinatorMode_RestoredOnRestart(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "coord", `---
name: coord
mode: coordinator
model: fake-model
---
Coordinator.`)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Start coordinator, shut down
	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, "", testWorkerFactory("hello"), nil)
	id, err := mgr1.StartAgent(ctx, "coord", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()

	// Restore — coordinator mode should survive (it's persistent)
	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, "", testWorkerFactory("hello"), nil)
	if err := mgr2.RestoreSessions(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, ok := mgr2.GetAgent(id)
	if !ok {
		t.Fatal("coordinator should be restored")
	}
	if info.Mode != config.ModeCoordinator {
		t.Errorf("restored mode = %q, want coordinator", info.Mode)
	}
}

func TestManager_CoordinatorMode_SessionNotCleaned(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "coord", `---
name: coord
mode: coordinator
model: fake-model
---
Coordinator.`)

	id, _ := mgr.StartAgent(t.Context(), "coord", "")
	sessDir := filepath.Join(dir, "sessions", id)

	mgr.StopAgent(id)

	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		t.Error("coordinator session dir should survive stop (like persistent)")
	}
}

func TestManager_CoordinatorTools_InSpawnConfig(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
mode: coordinator
model: fake-model
tools: [bash]
---
Coordinator.`)

	_, err := mgr.StartAgent(t.Context(), "coord", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Coordinator should have coordinator tools in effective tools
	if !cfg.EffectiveTools["start_agent"] {
		t.Error("coordinator should have start_agent in effective tools")
	}
	if !cfg.EffectiveTools["spawn_agent"] {
		t.Error("coordinator should have spawn_agent in effective tools")
	}
	if !cfg.EffectiveTools["memory_read"] {
		t.Error("coordinator should have memory_read in effective tools")
	}
}

func TestManager_CoordinatorGroups_InSpawnConfig(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	pool.SetCoordinatorGID(10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
mode: coordinator
model: fake-model
tools: [bash]
---
Coordinator.`)

	_, err := mgr.StartAgent(t.Context(), "coord", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Should have both primary group and coordinator group (order-independent).
	groupSet := make(map[uint32]bool)
	for _, g := range cfg.Groups {
		groupSet[g] = true
	}
	if !groupSet[10000] {
		t.Error("primary GID 10000 should be in groups")
	}
	if !groupSet[10001] {
		t.Error("coordinator GID 10001 should be in groups")
	}
	if len(cfg.Groups) != 2 {
		t.Errorf("expected 2 groups, got %v", cfg.Groups)
	}
}

func TestManager_CoordinatorMode_NoGroupConfigured(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	// CoordinatorGID not set — pool.CoordinatorGID() == 0
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
mode: coordinator
model: fake-model
tools: [bash]
---
Coordinator.`)

	_, err := mgr.StartAgent(t.Context(), "coord", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Only primary group — no coordinator group available
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected 1 group (primary only), got %v", cfg.Groups)
	}
	if cfg.Groups[0] != 10000 {
		t.Errorf("group should be primary (10000), got %d", cfg.Groups[0])
	}
}

func TestManager_NonCoordinator_NoCoordinatorGroup(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	pool.SetCoordinatorGID(10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "worker", `---
name: worker
mode: persistent
model: fake-model
tools: [bash]
---
Worker.`)

	_, err := mgr.StartAgent(t.Context(), "worker", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Should only have primary group, no coordinator group
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected 1 group, got %v", cfg.Groups)
	}
	if cfg.Groups[0] != 10000 {
		t.Errorf("group should be primary (10000), got %d", cfg.Groups[0])
	}
}

func TestManager_EphemeralAgent_NoCoordinatorTools(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "worker", `---
name: worker
mode: ephemeral
model: fake-model
tools: [bash]
---
Worker.`)

	// Start an ephemeral agent directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("worker"))
	mgr.startSession(t.Context(), "test-eph-id", cfg, "")

	spawnCfg := (*configs)[0]
	if spawnCfg.EffectiveTools["start_agent"] {
		t.Error("ephemeral agent should NOT have start_agent")
	}
	if !spawnCfg.EffectiveTools["spawn_agent"] {
		t.Error("ephemeral agent should have spawn_agent")
	}
	if spawnCfg.EffectiveTools["memory_read"] {
		t.Error("ephemeral agent should NOT have memory_read")
	}
}
