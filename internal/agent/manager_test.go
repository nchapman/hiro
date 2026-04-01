package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/ipc"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/uidpool"
)

// openTestPDB opens a platform DB in the given directory for testing.
func openTestPDB(t *testing.T, dir string) *platformdb.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0755); err != nil {
		t.Fatal(err)
	}
	pdb, err := platformdb.Open(filepath.Join(dir, "db", "hiro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Close() })
	return pdb
}

// testWorker implements ipc.AgentWorker for testing.
type testWorker struct {
	shutdown bool
	done     chan struct{}
	closed   bool
}

func (w *testWorker) ExecuteTool(_ context.Context, _, name, _ string) (ipc.ToolResult, error) {
	return ipc.ToolResult{Content: "mock result for " + name}, nil
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
func testWorkerFactory(_ string) WorkerFactory {
	return func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
		done := make(chan struct{})
		w := &testWorker{done: done}
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
	}, nil, logger, testWorkerFactory("hello from agent"), nil, nil)
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
	agents := mgr.ListInstances()
	if len(agents) != 0 {
		t.Errorf("new manager should have 0 agents, got %d", len(agents))
	}
}

func TestManager_CreateSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty agent ID")
	}

	info, ok := mgr.GetInstance(id)
	if !ok {
		t.Fatal("agent not found after start")
	}
	if info.Name != "test-agent" {
		t.Errorf("name = %q, want %q", info.Name, "test-agent")
	}
	if info.Mode != config.ModePersistent {
		t.Errorf("mode = %q, want persistent", info.Mode)
	}
}

func TestManager_CreateSession_MissingConfig(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.CreateInstance(t.Context(), "nonexistent", "", "persistent", "")
	if err == nil {
		t.Fatal("expected error for missing agent config")
	}
}

func TestManager_CreateSession_InvalidMode(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	for _, mode := range []string{"", "supercoordinator", "persistant"} {
		_, err := mgr.CreateInstance(t.Context(), "test-agent", "", mode, "")
		if err == nil {
			t.Errorf("mode %q: expected error, got nil", mode)
		}
	}
}

func TestManager_SendMessage_NoLoop(t *testing.T) {
	// Without a provider, no inference loop is created.
	// SendMessage should return an error indicating no loop.
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	_, err = mgr.SendMessage(t.Context(), id, "hi", nil)
	if err == nil {
		t.Fatal("expected error when no inference loop")
	}
	if !strings.Contains(err.Error(), "no inference loop") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestManager_StopSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	info, err := mgr.StopInstance(id)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if info.Name != "test-agent" {
		t.Errorf("stopped name = %q, want test-agent", info.Name)
	}

	// Persistent agents stay in registry with "stopped" status.
	ai, ok := mgr.GetInstance(id)
	if !ok {
		t.Fatal("persistent agent should still be in registry after stop")
	}
	if ai.Status != InstanceStatusStopped {
		t.Errorf("status = %q, want %q", ai.Status, InstanceStatusStopped)
	}
}

func TestManager_StopSession_Ephemeral(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "eph-agent", `---
name: eph-agent
model: fake-model
---
Ephemeral agent.`)

	id, err := mgr.CreateInstance(t.Context(), "eph-agent", "", "ephemeral", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	mgr.StopInstance(id)

	// Ephemeral agents are fully removed after stop.
	if _, ok := mgr.GetInstance(id); ok {
		t.Error("ephemeral agent should not exist after stop")
	}
}

func TestManager_StopSession_WithChildren(t *testing.T) {
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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "")

	// Stopping parent should also stop child (both persistent → soft-stopped).
	mgr.StopInstance(parentID)

	parentInfo, ok := mgr.GetInstance(parentID)
	if !ok {
		t.Fatal("parent should still be in registry")
	}
	if parentInfo.Status != InstanceStatusStopped {
		t.Errorf("parent status = %q, want stopped", parentInfo.Status)
	}

	childInfo, ok := mgr.GetInstance(childID)
	if !ok {
		t.Fatal("child should still be in registry")
	}
	if childInfo.Status != InstanceStatusStopped {
		t.Errorf("child status = %q, want stopped", childInfo.Status)
	}
}

func TestManager_DeleteSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := mgr.DeleteInstance(id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, ok := mgr.GetInstance(id); ok {
		t.Error("agent should not exist after delete")
	}
}

func TestManager_StartSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	mgr.StopInstance(id)
	ai, _ := mgr.GetInstance(id)
	if ai.Status != InstanceStatusStopped {
		t.Fatalf("status after stop = %q, want stopped", ai.Status)
	}

	if err := mgr.StartInstance(t.Context(), id); err != nil {
		t.Fatalf("restart: %v", err)
	}

	ai, ok := mgr.GetInstance(id)
	if !ok {
		t.Fatal("agent should exist after restart")
	}
	if ai.Status != InstanceStatusRunning {
		t.Errorf("status after restart = %q, want running", ai.Status)
	}
}

func TestManager_StopSession_AlreadyStopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	mgr.StopInstance(id)

	// Stopping an already-stopped agent should be a no-op.
	info, err := mgr.StopInstance(id)
	if err != nil {
		t.Fatalf("stop already-stopped: %v", err)
	}
	if info.Status != string(InstanceStatusStopped) {
		t.Errorf("status = %q, want stopped", info.Status)
	}
}

func TestManager_StopSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.StopInstance("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for stopping nonexistent agent")
	}
}

func TestManager_DeleteSession_Stopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	mgr.StopInstance(id)

	if err := mgr.DeleteInstance(id); err != nil {
		t.Fatalf("delete stopped agent: %v", err)
	}
	if _, ok := mgr.GetInstance(id); ok {
		t.Error("agent should not exist after delete")
	}
}

func TestManager_DeleteSession_WithChildren(t *testing.T) {
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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "")

	if err := mgr.DeleteInstance(parentID); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if _, ok := mgr.GetInstance(parentID); ok {
		t.Error("parent should be deleted")
	}
	if _, ok := mgr.GetInstance(childID); ok {
		t.Error("child should be deleted with parent")
	}
}

func TestManager_DeleteSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.DeleteInstance("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for deleting nonexistent agent")
	}
}

func TestManager_StartSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.StartInstance(t.Context(), "nonexistent-id")
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("expected ErrInstanceNotFound, got %v", err)
	}
}

func TestManager_StartSession_AlreadyRunning(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")

	err := mgr.StartInstance(t.Context(), id)
	if !errors.Is(err, ErrInstanceNotStopped) {
		t.Fatalf("expected ErrInstanceNotStopped, got %v", err)
	}
}

func TestManager_StartSession_ErrorRecovery(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	mgr.StopInstance(id)

	// Delete the agent definition so startSession will fail on config load.
	os.RemoveAll(mgr.agentDefDir("test-agent"))

	err := mgr.StartInstance(t.Context(), id)
	if err == nil {
		t.Fatal("expected error when agent definition is missing")
	}

	// Session should still be visible as stopped (not lost from registry).
	info, ok := mgr.GetInstance(id)
	if !ok {
		t.Fatal("session should still be in registry after failed restart")
	}
	if info.Status != InstanceStatusStopped {
		t.Errorf("status = %q, want stopped", info.Status)
	}
}

func TestManager_SendMessage_Stopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	mgr.StopInstance(id)

	_, err := mgr.SendMessage(t.Context(), id, "hello", nil)
	if err == nil {
		t.Fatal("expected error for messaging stopped agent")
	}
	if !strings.Contains(err.Error(), "stopped") {
		t.Errorf("error = %q, want 'stopped'", err)
	}
}

func TestManager_SessionByAgentName(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")

	found, ok := mgr.InstanceByAgentName("test-agent")
	if !ok {
		t.Fatal("expected to find agent by name")
	}
	if found != id {
		t.Errorf("found ID = %q, want %q", found, id)
	}
}

func TestManager_SessionByAgentName_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, found := mgr.InstanceByAgentName("nope")
	if found {
		t.Error("expected not found")
	}
}

func TestManager_GetSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, found := mgr.GetInstance("nonexistent-id")
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

func TestManager_ListSessions(t *testing.T) {
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

	mgr.CreateInstance(t.Context(), "a1", "", "persistent", "")
	mgr.CreateInstance(t.Context(), "a2", "", "persistent", "")

	agents := mgr.ListInstances()
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

	mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	mgr.Shutdown()

	if len(mgr.ListInstances()) != 0 {
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

	rootID, _ := mgr.CreateInstance(t.Context(), "root", "", "persistent", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", rootID, "persistent", "")

	info, _ := mgr.GetInstance(childID)
	if info.ParentID != rootID {
		t.Errorf("child parentID = %q, want %q", info.ParentID, rootID)
	}
}

func TestManager_AgentDefDir(t *testing.T) {
	mgr, dir := setupTestManager(t)
	got := mgr.agentDefDir("coordinator")
	want := filepath.Join(dir, "agents", "coordinator", "")
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

	rootID, _ := mgr.CreateInstance(t.Context(), "root", "", "persistent", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", rootID, "persistent", "")

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

func TestManager_ListChildSessions(t *testing.T) {
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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "")
	mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "")

	// ListChildren scoped to parent
	children := mgr.ListChildInstances(parentID)
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if children[0].Name != "child" {
		t.Errorf("child name = %q, want child", children[0].Name)
	}

	// ListChildren for agent with no children
	noKids := mgr.ListChildInstances("nonexistent")
	if len(noKids) != 0 {
		t.Errorf("expected 0 children, got %d", len(noKids))
	}
}

func TestManager_SessionDirCreated(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	sessDir := filepath.Join(dir, "instances", id)
	if _, err := os.Stat(sessDir); err != nil {
		t.Fatalf("session dir should exist at %s: %v", sessDir, err)
	}
}

func TestManager_SessionSubdirs(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	instDir := filepath.Join(dir, "instances", id)

	// Instance dir should exist.
	if _, err := os.Stat(instDir); err != nil {
		t.Fatalf("instance dir should exist: %v", err)
	}

	// Session dir should exist under instance dir.
	sessionsDir := filepath.Join(instDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("sessions dir should exist: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 session dir, got %d", len(entries))
	}
	sessDir := filepath.Join(sessionsDir, entries[0].Name())
	for _, sub := range []string{"scratch", "tmp"} {
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
	parentID, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")

	// Start an ephemeral child directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("test-agent"))
	ephID, _ := mgr.startInstance(t.Context(), "ephemeral-test-id", "ephemeral-sess-id", cfg, parentID, config.ModeEphemeral, "")

	sessDir := filepath.Join(dir, "instances", ephID)
	if _, err := os.Stat(sessDir); err != nil {
		t.Fatalf("ephemeral session dir should exist: %v", err)
	}

	mgr.StopInstance(ephID)

	if _, err := os.Stat(sessDir); !os.IsNotExist(err) {
		t.Error("ephemeral session dir should be cleaned up after stop")
	}
}

func TestManager_PersistentNotCleaned(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	sessDir := filepath.Join(dir, "instances", id)

	mgr.StopInstance(id)

	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		t.Error("persistent session dir should survive stop")
	}
}

func TestManager_RestoreSessions(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	// Create a manager, start an agent, then shut down
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()
	mgr1 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, testWorkerFactory("hello"), nil, pdb)

	id, err := mgr1.CreateInstance(ctx, "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()

	// Create a new manager and restore
	mgr2 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The agent should be running with the same ID
	info, ok := mgr2.GetInstance(id)
	if !ok {
		t.Fatal("restored agent not found")
	}
	if info.Name != "test-agent" {
		t.Errorf("restored name = %q, want test-agent", info.Name)
	}
}

func TestManager_RestoreSessions_Stopped(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)

	// Create a manager, start an agent, stop it, then shut down.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()
	mgr1 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, testWorkerFactory("hello"), nil, pdb)

	id, err := mgr1.CreateInstance(ctx, "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.StopInstance(id)
	mgr1.Shutdown()

	// Create a new manager and restore.
	mgr2 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The agent should be restored as stopped, not running.
	info, ok := mgr2.GetInstance(id)
	if !ok {
		t.Fatal("stopped agent not found after restore")
	}
	if info.Status != InstanceStatusStopped {
		t.Errorf("restored status = %q, want stopped", info.Status)
	}

	// Restarting it should work and clear the stopped state.
	if err := mgr2.StartInstance(ctx, id); err != nil {
		t.Fatalf("restart: %v", err)
	}
	info, _ = mgr2.GetInstance(id)
	if info.Status != InstanceStatusRunning {
		t.Errorf("status after restart = %q, want running", info.Status)
	}
}

func TestManager_SpawnSession_NoLoop(t *testing.T) {
	// SpawnSession calls SendMessage internally which requires a loop.
	// Without a provider, this fails gracefully.
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.SpawnEphemeral(t.Context(), "test-agent", "do something", "", "", nil)
	if err == nil {
		t.Fatal("expected error when no inference loop")
	}

	// Ephemeral agent should be cleaned up even on failure.
	agents := mgr.ListInstances()
	if len(agents) != 0 {
		t.Errorf("expected 0 agents after spawn, got %d", len(agents))
	}
}

// --- UID pool integration tests ---

// capturingWorkerFactory creates workers and records the SpawnConfig for each.
func capturingWorkerFactory(_ string) (WorkerFactory, *[]ipc.SpawnConfig) {
	var configs []ipc.SpawnConfig
	factory := func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error) {
		configs = append(configs, cfg)
		done := make(chan struct{})
		w := &testWorker{done: done}
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
	}, nil, logger, factory, pool, nil)
	return mgr, dir, configs
}

func TestManager_UIDPool_Assigned(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
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

	mgr.CreateInstance(t.Context(), "a", "", "persistent", "")
	mgr.CreateInstance(t.Context(), "b", "", "persistent", "")

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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if pool.InUse() != 1 {
		t.Fatalf("expected 1 UID in use, got %d", pool.InUse())
	}

	mgr.StopInstance(id)
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs in use after stop, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_ReleasedOnShutdown(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, _ := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
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
	}, nil, logger, failingWorkerFactory(), pool, nil)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
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

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}
	_, err = mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	_, err = mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "")
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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "")
	mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "")
	if pool.InUse() != 2 {
		t.Fatalf("expected 2 UIDs, got %d", pool.InUse())
	}

	// Stop parent — should release both parent and child UIDs
	mgr.StopInstance(parentID)
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs after stopping parent+child, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_RestoreSessions(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Start with a pool, create an agent, shut down
	factory1, _ := capturingWorkerFactory("hello")
	mgr1 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, factory1, pool, pdb)

	id, err := mgr1.CreateInstance(ctx, "test-agent", "", "persistent", "")
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
	}, nil, logger, factory2, pool2, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
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
	info, ok := mgr2.GetInstance(id)
	if !ok {
		t.Fatal("restored agent not found")
	}
	if info.Name != "test-agent" {
		t.Errorf("restored name = %q, want test-agent", info.Name)
	}
}

func TestManager_UIDPool_SpawnSubagent(t *testing.T) {
	// SpawnSession creates an ephemeral agent with a UID, then cleans up.
	// Without a provider, SendMessage fails, but cleanup should still release the UID.
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, _ = mgr.SpawnEphemeral(t.Context(), "test-agent", "do something", "", "", nil)
	// Error expected (no loop), but cleanup should happen.

	if len(*configs) != 1 {
		t.Fatalf("expected 1 spawn config, got %d", len(*configs))
	}
	if (*configs)[0].UID == 0 {
		t.Fatal("ephemeral agent should have non-zero UID")
	}

	// UID should be released after subagent cleanup.
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
  - Bash
  - Read
  - Write
---
Agent with tools.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("tooled"))
	effective := mgr.computeEffectiveTools(cfg, "")

	if !effective["Bash"] || !effective["Read"] || !effective["Write"] {
		t.Errorf("effective tools should include all declared tools, got %v", effective)
	}
	if effective["Glob"] {
		t.Error("Glob should not be in effective tools (not declared)")
	}
}

func TestBuildAllowedToolsMap_EphemeralMode(t *testing.T) {
	effective := map[string]bool{"Bash": true, "Read": true}
	allowed := buildAllowedToolsMap(effective, config.ModeEphemeral, false)

	// Ephemeral agents get SpawnInstance but NOT coordinator or persistent tools.
	if !allowed["SpawnInstance"] {
		t.Error("ephemeral agents should get SpawnInstance")
	}
	if allowed["ResumeInstance"] || allowed["StopInstance"] || allowed["DeleteInstance"] || allowed["SendMessage"] || allowed["ListInstances"] {
		t.Error("ephemeral agents should not get coordinator tools")
	}
	if allowed["TodoWrite"] {
		t.Error("ephemeral agents should not get persistent tools")
	}
}

func TestBuildAllowedToolsMap_PersistentMode(t *testing.T) {
	effective := map[string]bool{"Bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModePersistent, false)

	// Persistent agents get SpawnInstance + persistent tools, but NOT coordinator tools.
	if !allowed["SpawnInstance"] {
		t.Error("persistent agents should get SpawnInstance")
	}
	if !allowed["TodoWrite"] || !allowed["HistorySearch"] || !allowed["HistoryRecall"] {
		t.Error("persistent agents should get persistent tools")
	}
	if allowed["CreatePersistentInstance"] || allowed["ResumeInstance"] || allowed["StopInstance"] || allowed["SendMessage"] || allowed["ListInstances"] {
		t.Error("persistent agents should not get coordinator tools")
	}
}

func TestBuildAllowedToolsMap_CoordinatorMode(t *testing.T) {
	effective := map[string]bool{"Bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModeCoordinator, false)

	// Coordinator agents get everything: spawn + coordinator + persistent tools.
	if !allowed["SpawnInstance"] {
		t.Error("coordinators should get SpawnInstance")
	}
	if !allowed["CreatePersistentInstance"] || !allowed["ResumeInstance"] || !allowed["StopInstance"] || !allowed["DeleteInstance"] || !allowed["SendMessage"] || !allowed["ListInstances"] {
		t.Error("coordinators should get coordinator tools")
	}
	if !allowed["TodoWrite"] || !allowed["HistorySearch"] || !allowed["HistoryRecall"] {
		t.Error("coordinators should get persistent tools")
	}
}

func TestBuildAllowedToolsMap_WithSkills(t *testing.T) {
	effective := map[string]bool{"Bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModeEphemeral, true)
	if !allowed["Skill"] {
		t.Error("Skill should be included when hasSkills is true")
	}
}

func TestManager_CoordinatorMode_RestoredOnRestart(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "coord", `---
name: coord
model: fake-model
---
Coordinator.`)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Start coordinator, shut down
	pdb := openTestPDB(t, dir)
	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	id, err := mgr1.CreateInstance(ctx, "coord", "", "coordinator", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()

	// Restore — coordinator mode should survive (it's persistent)
	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, ok := mgr2.GetInstance(id)
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
model: fake-model
---
Coordinator.`)

	id, _ := mgr.CreateInstance(t.Context(), "coord", "", "coordinator", "")
	sessDir := filepath.Join(dir, "instances", id)

	mgr.StopInstance(id)

	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		t.Error("coordinator session dir should survive stop (like persistent)")
	}
}

func TestManager_CoordinatorTools_InSpawnConfig(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
tools: [Bash]
---
Coordinator.`)

	_, err := mgr.CreateInstance(t.Context(), "coord", "", "coordinator", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Coordinator should have coordinator tools in effective tools
	if !cfg.EffectiveTools["DeleteInstance"] {
		t.Error("coordinator should have DeleteInstance in effective tools")
	}
	if !cfg.EffectiveTools["SpawnInstance"] {
		t.Error("coordinator should have SpawnInstance in effective tools")
	}
	if !cfg.EffectiveTools["TodoWrite"] {
		t.Error("coordinator should have TodoWrite in effective tools")
	}
}

func TestManager_CoordinatorGroups_InSpawnConfig(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	pool.SetCoordinatorGID(10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
model: fake-model
tools: [Bash]
---
Coordinator.`)

	_, err := mgr.CreateInstance(t.Context(), "coord", "", "coordinator", "")
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
model: fake-model
tools: [Bash]
---
Coordinator.`)

	_, err := mgr.CreateInstance(t.Context(), "coord", "", "coordinator", "")
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
model: fake-model
tools: [Bash]
---
Worker.`)

	_, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "")
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
model: fake-model
tools: [Bash]
---
Worker.`)

	// Start an ephemeral agent directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("worker"))
	mgr.startInstance(t.Context(), "test-eph-id", "test-eph-sess-id", cfg, "", config.ModeEphemeral, "")

	spawnCfg := (*configs)[0]
	if !spawnCfg.EffectiveTools["SpawnInstance"] {
		t.Error("ephemeral agent should have SpawnInstance")
	}
	if spawnCfg.EffectiveTools["TodoWrite"] {
		t.Error("ephemeral should NOT have TodoWrite")
	}
}

// --- Config push tests ---

func TestExtractAgentName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"agents/foo/agent.md", "foo"},
		{"agents/my-agent/agent.md", "my-agent"},
		{"agents/bar/skills/review.md", "bar"},
		{"other/foo/agent.md", ""},
		{"agents", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := extractAgentName(tt.path); got != tt.want {
			t.Errorf("extractAgentName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestManager_PushConfigUpdate(t *testing.T) {
	mgr, dir := setupTestManager(t)

	agentDir := filepath.Join(dir, "agents", "worker")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: Old desc\ntools: [Bash, Read]\n---\nWork."), 0644)

	id, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "")
	if err != nil {
		t.Fatal(err)
	}

	// Update agent.md
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: New desc\ntools: [Bash, Read, Grep]\n---\nUpdated work."), 0644)
	mgr.pushConfigUpdate("worker")

	// Verify the description was updated in-memory.
	info, ok := mgr.GetInstance(id)
	if !ok {
		t.Fatal("session not found")
	}
	if info.Description != "New desc" {
		t.Errorf("description = %q, want %q", info.Description, "New desc")
	}
}

func TestManager_PushConfigUpdate_SkipsStopped(t *testing.T) {
	mgr, dir := setupTestManager(t)

	// Write agent definition
	agentDir := filepath.Join(dir, "agents", "worker")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\n---\nWork."), 0644)

	// Start and stop a session
	id, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "")
	if err != nil {
		t.Fatal(err)
	}
	mgr.StopInstance(id)

	// Push — should not crash or send to stopped agent
	mgr.pushConfigUpdate("worker")

	// Verify no update was sent (worker is nil after stop)
	mgr.mu.RLock()
	s := mgr.instances[id]
	mgr.mu.RUnlock()

	if s.info.Status != InstanceStatusStopped {
		t.Error("expected session to be stopped")
	}
}

func TestManager_PushConfigUpdate_UpdatesDescription(t *testing.T) {
	mgr, dir := setupTestManager(t)

	agentDir := filepath.Join(dir, "agents", "worker")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: Old desc\n---\nWork."), 0644)

	id, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "")
	if err != nil {
		t.Fatal(err)
	}

	// Update description
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: New desc\n---\nWork."), 0644)
	mgr.pushConfigUpdate("worker")

	info, ok := mgr.GetInstance(id)
	if !ok {
		t.Fatal("session not found")
	}
	if info.Description != "New desc" {
		t.Errorf("description = %q, want %q", info.Description, "New desc")
	}
}

func TestManager_PushConfigUpdateAll(t *testing.T) {
	mgr, dir := setupTestManager(t)

	for _, name := range []string{"alpha", "beta"} {
		agentDir := filepath.Join(dir, "agents", name)
		os.MkdirAll(agentDir, 0755)
		os.WriteFile(filepath.Join(agentDir, "agent.md"),
			[]byte("---\nname: "+name+"\ndescription: old\ntools: [Bash]\n---\nDo stuff."), 0644)
	}

	idA, err := mgr.CreateInstance(t.Context(), "alpha", "", "persistent", "")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := mgr.CreateInstance(t.Context(), "beta", "", "persistent", "")
	if err != nil {
		t.Fatal(err)
	}

	// Update descriptions.
	for _, name := range []string{"alpha", "beta"} {
		agentDir := filepath.Join(dir, "agents", name)
		os.WriteFile(filepath.Join(agentDir, "agent.md"),
			[]byte("---\nname: "+name+"\ndescription: updated\ntools: [Bash]\n---\nDo stuff."), 0644)
	}

	mgr.PushConfigUpdateAll()

	// Descriptions should be updated in-memory.
	for _, id := range []string{idA, idB} {
		info, ok := mgr.GetInstance(id)
		if !ok {
			t.Errorf("session %s not found", id)
			continue
		}
		if info.Description != "updated" {
			t.Errorf("session %s description = %q, want %q", id, info.Description, "updated")
		}
	}
}
