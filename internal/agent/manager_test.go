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
	"github.com/nchapman/hiro/internal/models"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/toolrules"
	"github.com/nchapman/hiro/internal/uidpool"
)

// openTestPDB opens a platform DB in the given directory for testing.
func openTestPDB(t *testing.T, dir string) *platformdb.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
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
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte(content), 0o644); err != nil {
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

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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
	_, err := mgr.CreateInstance(t.Context(), "nonexistent", "", "persistent", "", "", "", "")
	if err == nil {
		t.Fatal("expected error for missing agent config")
	}
}

func TestManager_CreateSession_InvalidMode(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	for _, mode := range []string{"", "operator", "superoperator", "invalid-mode"} {
		_, err := mgr.CreateInstance(t.Context(), "test-agent", "", mode, "", "", "", "")
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

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, err := mgr.CreateInstance(t.Context(), "eph-agent", "", "ephemeral", "", "", "", "")
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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "", "", "", "")

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

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "", "", "", "")

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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")

	err := mgr.StartInstance(t.Context(), id)
	if !errors.Is(err, ErrInstanceNotStopped) {
		t.Fatalf("expected ErrInstanceNotStopped, got %v", err)
	}
}

func TestManager_StartSession_ErrorRecovery(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")

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

	mgr.CreateInstance(t.Context(), "a1", "", "persistent", "", "", "", "")
	mgr.CreateInstance(t.Context(), "a2", "", "persistent", "", "", "", "")

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

	mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	rootID, _ := mgr.CreateInstance(t.Context(), "root", "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", rootID, "persistent", "", "", "", "")

	info, _ := mgr.GetInstance(childID)
	if info.ParentID != rootID {
		t.Errorf("child parentID = %q, want %q", info.ParentID, rootID)
	}
}

func TestManager_AgentDefDir(t *testing.T) {
	mgr, dir := setupTestManager(t)
	got := mgr.agentDefDir("operator")
	want := filepath.Join(dir, "agents", "operator", "")
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
		{"operator", false},
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

	rootID, _ := mgr.CreateInstance(t.Context(), "root", "", "persistent", "", "", "", "")
	childID, _ := mgr.CreateInstance(t.Context(), "child", rootID, "persistent", "", "", "", "")

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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "", "", "", "")
	mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "", "", "", "")

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

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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
	parentID, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")

	// Start an ephemeral child directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("test-agent"))
	ephID, _ := mgr.startInstance(t.Context(), "ephemeral-test-id", "ephemeral-sess-id", cfg, parentID, config.ModeEphemeral, "", "", "", "")

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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	id, err := mgr1.CreateInstance(ctx, "test-agent", "", "persistent", "", "", "", "")
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

	id, err := mgr1.CreateInstance(ctx, "test-agent", "", "persistent", "", "", "", "")
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

func TestManager_Restore_ParentChildHierarchy(t *testing.T) {
	// Stopped parent and stopped child should both restore and maintain lineage.
	dir := t.TempDir()
	writeAgentMD(t, dir, "parent-agent", `---
name: parent-agent
allowed_tools: [Bash]
---
Parent.`)
	writeAgentMD(t, dir, "child-agent", `---
name: child-agent
allowed_tools: [Bash]
---
Child.`)
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	parentID, _ := mgr1.CreateInstance(ctx, "parent-agent", "", "persistent", "", "", "", "")
	childID, _ := mgr1.CreateInstance(ctx, "child-agent", parentID, "persistent", "", "", "", "")
	mgr1.StopInstance(childID)
	mgr1.StopInstance(parentID)
	mgr1.Shutdown()

	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	parentInfo, ok := mgr2.GetInstance(parentID)
	if !ok {
		t.Fatal("parent not restored")
	}
	if parentInfo.Status != InstanceStatusStopped {
		t.Errorf("parent status = %q, want stopped", parentInfo.Status)
	}

	childInfo, ok := mgr2.GetInstance(childID)
	if !ok {
		t.Fatal("child not restored")
	}
	if childInfo.ParentID != parentID {
		t.Errorf("child parent = %q, want %q", childInfo.ParentID, parentID)
	}
}

func TestManager_Restore_StoppedGroupInheritance(t *testing.T) {
	// Stopped child should only get groups held by its stopped parent.
	dir := t.TempDir()
	writeAgentMD(t, dir, "coord", `---
name: coord
allowed_tools: [Bash]
groups: [hiro-operators]
---
Operator.`)
	writeAgentMD(t, dir, "worker", `---
name: worker
allowed_tools: [Bash]
groups: [hiro-operators]
---
Worker that also wants hiro-operators.`)
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	pool1 := uidpool.New(10000, 10000, 64)
	pool1.SetGroupGID("hiro-operators", 10001)
	factory1, _ := capturingWorkerFactory("hello")
	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, factory1, pool1, pdb)

	parentID, _ := mgr1.CreateInstance(ctx, "coord", "", "persistent", "", "", "", "")
	childID, _ := mgr1.CreateInstance(ctx, "worker", parentID, "persistent", "", "", "", "")
	mgr1.StopInstance(childID)
	mgr1.StopInstance(parentID)
	mgr1.Shutdown()

	// Restore with fresh pool — both stopped, child should inherit parent's groups.
	pool2 := uidpool.New(10000, 10000, 64)
	pool2.SetGroupGID("hiro-operators", 10001)
	factory2, configs2 := capturingWorkerFactory("hello")
	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, factory2, pool2, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Now start the child — it should get hiro-operators (parent has it).
	if err := mgr2.StartInstance(ctx, childID); err != nil {
		t.Fatalf("start child: %v", err)
	}

	if len(*configs2) != 1 {
		t.Fatalf("expected 1 spawn config, got %d", len(*configs2))
	}
	childCfg := (*configs2)[0]
	groupSet := make(map[uint32]bool)
	for _, g := range childCfg.Groups {
		groupSet[g] = true
	}
	if !groupSet[10001] {
		t.Error("restored child should have hiro-operators (parent has it)")
	}
}

func TestManager_Restore_StoppedGroupEscalationBlocked(t *testing.T) {
	// Stopped child with groups NOT held by stopped parent should be denied.
	dir := t.TempDir()
	writeAgentMD(t, dir, "unpriv", `---
name: unpriv
allowed_tools: [Bash]
---
No groups.`)
	writeAgentMD(t, dir, "wants-groups", `---
name: wants-groups
allowed_tools: [Bash]
groups: [hiro-operators]
---
Wants escalation.`)
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	pool1 := uidpool.New(10000, 10000, 64)
	pool1.SetGroupGID("hiro-operators", 10001)
	factory1, _ := capturingWorkerFactory("hello")
	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, factory1, pool1, pdb)

	parentID, _ := mgr1.CreateInstance(ctx, "unpriv", "", "persistent", "", "", "", "")
	childID, _ := mgr1.CreateInstance(ctx, "wants-groups", parentID, "persistent", "", "", "", "")
	mgr1.StopInstance(childID)
	mgr1.StopInstance(parentID)
	mgr1.Shutdown()

	// Restore with fresh pool.
	pool2 := uidpool.New(10000, 10000, 64)
	pool2.SetGroupGID("hiro-operators", 10001)
	factory2, configs2 := capturingWorkerFactory("hello")
	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, factory2, pool2, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Start the child — should NOT get hiro-operators (parent doesn't have it).
	if err := mgr2.StartInstance(ctx, childID); err != nil {
		t.Fatalf("start child: %v", err)
	}

	if len(*configs2) != 1 {
		t.Fatalf("expected 1 spawn config, got %d", len(*configs2))
	}
	childCfg := (*configs2)[0]
	for _, g := range childCfg.Groups {
		if g == 10001 {
			t.Error("restored child should NOT have hiro-operators (parent doesn't have it)")
		}
	}
}

func TestManager_Restore_RunningInstanceGroups(t *testing.T) {
	// Running instance should be restored with correct groups via startInstance.
	dir := t.TempDir()
	writeAgentMD(t, dir, "coord", `---
name: coord
allowed_tools: [Bash]
groups: [hiro-operators]
---
Operator.`)
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	pool1 := uidpool.New(10000, 10000, 64)
	pool1.SetGroupGID("hiro-operators", 10001)
	factory1, _ := capturingWorkerFactory("hello")
	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, factory1, pool1, pdb)
	mgr1.CreateInstance(ctx, "coord", "", "persistent", "", "", "", "")
	mgr1.Shutdown()

	// Restore — running instances go through startInstance.
	pool2 := uidpool.New(10000, 10000, 64)
	pool2.SetGroupGID("hiro-operators", 10001)
	factory2, configs2 := capturingWorkerFactory("hello")
	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, factory2, pool2, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if len(*configs2) != 1 {
		t.Fatalf("expected 1 spawn config, got %d", len(*configs2))
	}
	cfg := (*configs2)[0]
	groupSet := make(map[uint32]bool)
	for _, g := range cfg.Groups {
		groupSet[g] = true
	}
	if !groupSet[10001] {
		t.Error("restored running instance should have hiro-operators")
	}
}

func TestManager_Restore_EphemeralCleaned(t *testing.T) {
	// Ephemeral instances in the DB should be cleaned up on restore.
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Manually insert an ephemeral instance into the DB.
	pdb.CreateInstance(ctx, platformdb.Instance{
		ID:        "eph-orphan",
		AgentName: "test-agent",
		Mode:      "ephemeral",
	})

	mgr := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Ephemeral should have been cleaned from DB.
	_, err := pdb.GetInstance(ctx, "eph-orphan")
	if err == nil {
		t.Error("ephemeral instance should have been cleaned from DB")
	}
}

func TestManager_Restore_MissingAgentDefSkipped(t *testing.T) {
	// Instance with missing agent definition should be skipped, not crash.
	dir := t.TempDir()
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Insert a persistent instance for a non-existent agent.
	pdb.CreateInstance(ctx, platformdb.Instance{
		ID:        "orphan-inst",
		AgentName: "deleted-agent",
		Mode:      "persistent",
	})

	mgr := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore should not fail: %v", err)
	}

	// Instance should not be registered (agent def is missing).
	if _, ok := mgr.GetInstance("orphan-inst"); ok {
		t.Error("instance with missing agent def should not be restored")
	}
}

func TestManager_Restore_MissingInstanceDirCleaned(t *testing.T) {
	// Running instance with missing dir should be cleaned from DB.
	dir := t.TempDir()
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Insert a running persistent instance but don't create the instance dir.
	pdb.CreateInstance(ctx, platformdb.Instance{
		ID:        "no-dir-inst",
		AgentName: "test-agent",
		Mode:      "persistent",
		Status:    "running",
	})

	mgr := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore should not fail: %v", err)
	}

	// Instance should have been removed from DB.
	_, err := pdb.GetInstance(ctx, "no-dir-inst")
	if err == nil {
		t.Error("running instance with missing dir should have been cleaned from DB")
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

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	mgr.CreateInstance(t.Context(), "a", "", "persistent", "", "", "", "")
	mgr.CreateInstance(t.Context(), "b", "", "persistent", "", "", "", "")

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

	id, _ := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	_, err := mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}
	_, err = mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	_, err = mgr.CreateInstance(t.Context(), "test-agent", "", "persistent", "", "", "", "")
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

	parentID, _ := mgr.CreateInstance(t.Context(), "parent", "", "persistent", "", "", "", "")
	mgr.CreateInstance(t.Context(), "child", parentID, "persistent", "", "", "", "")
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

	id, err := mgr1.CreateInstance(ctx, "test-agent", "", "persistent", "", "", "", "")
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
allowed_tools:
  - Bash
  - Read
  - Write
---
Agent with tools.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("tooled"))
	effective, _, _, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatalf("computeEffectiveTools: %v", err)
	}

	if !effective["Bash"] || !effective["Read"] || !effective["Write"] {
		t.Errorf("effective tools should include all declared tools, got %v", effective)
	}
	if effective["Glob"] {
		t.Error("Glob should not be in effective tools (not declared)")
	}
}

func TestManager_EffectiveTools_Parameterized(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "restricted", `---
name: restricted
model: fake-model
allowed_tools:
  - Bash(curl *)
  - Read
disallowed_tools:
  - Bash(curl *--upload*)
---
Agent with parameterized tools.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("restricted"))
	effective, allowLayers, denyRules, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatalf("computeEffectiveTools: %v", err)
	}

	// Tool names should be in the effective set.
	if !effective["Bash"] || !effective["Read"] {
		t.Errorf("effective tools should include Bash and Read, got %v", effective)
	}

	// Should have an allow layer (Bash has parameterized rule).
	if len(allowLayers) != 1 {
		t.Fatalf("expected 1 allow layer, got %d", len(allowLayers))
	}
	// Allow layer should have 2 rules (Bash(curl *) and Read).
	if len(allowLayers[0]) != 2 {
		t.Errorf("expected 2 rules in allow layer, got %d", len(allowLayers[0]))
	}

	// Should have 1 deny rule.
	if len(denyRules) != 1 {
		t.Fatalf("expected 1 deny rule, got %d", len(denyRules))
	}
	if denyRules[0].Tool != "Bash" || denyRules[0].Pattern != "curl *--upload*" {
		t.Errorf("unexpected deny rule: %v", denyRules[0])
	}
}

func TestManager_EffectiveTools_WholeDenyRemovesTool(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "nodeny", `---
name: nodeny
model: fake-model
allowed_tools:
  - Bash
  - Read
  - Write
disallowed_tools:
  - Write
---
Agent where Write is fully denied.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("nodeny"))
	effective, _, _, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatalf("computeEffectiveTools: %v", err)
	}

	if effective["Write"] {
		t.Error("Write should be removed from effective set by whole-tool deny")
	}
	if !effective["Bash"] || !effective["Read"] {
		t.Error("Bash and Read should remain in effective set")
	}
}

func TestManager_EffectiveTools_WholeToolStrippedWhenParameterized(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "mixed", `---
name: mixed
model: fake-model
allowed_tools:
  - Bash
  - Bash(curl *)
  - Read
---
Agent with both whole-tool and parameterized Bash.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("mixed"))
	_, allowLayers, _, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatalf("computeEffectiveTools: %v", err)
	}

	if len(allowLayers) != 1 {
		t.Fatalf("expected 1 allow layer, got %d", len(allowLayers))
	}

	// The layer should NOT contain whole-tool Bash (it would nullify Bash(curl *)).
	for _, r := range allowLayers[0] {
		if r.Tool == "Bash" && r.IsWholeTool() {
			t.Error("whole-tool Bash should be stripped when Bash(curl *) exists in same layer")
		}
	}

	// Should still contain Bash(curl *) and Read.
	hasBashCurl := false
	hasRead := false
	for _, r := range allowLayers[0] {
		if r.Tool == "Bash" && r.Pattern == "curl *" {
			hasBashCurl = true
		}
		if r.Tool == "Read" && r.IsWholeTool() {
			hasRead = true
		}
	}
	if !hasBashCurl {
		t.Error("Bash(curl *) should remain in the layer")
	}
	if !hasRead {
		t.Error("Read should remain in the layer")
	}
}

// --- computeEffectiveTools with CP and parent ---

// mockCP implements ControlPlane for testing tool intersection.
type mockCP struct {
	tools     map[string][]string // agent name → allowed tools
	denyTools map[string][]string // agent name → disallowed tools
}

func (m *mockCP) AgentTools(name string) ([]string, bool) {
	t, ok := m.tools[name]
	return t, ok
}
func (m *mockCP) AgentDisallowedTools(name string) []string { return m.denyTools[name] }
func (m *mockCP) SecretNames() []string                     { return nil }
func (m *mockCP) SecretEnv() []string                       { return nil }
func (m *mockCP) ProviderInfo() (string, string, string, bool) {
	return "", "", "", false
}
func (m *mockCP) ProviderByType(string) (string, string, bool) { return "", "", false }
func (m *mockCP) ConfiguredProviderTypes() []string            { return nil }
func (m *mockCP) DefaultModelSpec() models.ModelSpec           { return models.ModelSpec{} }
func (m *mockCP) ResolveSecret(value string) string            { return value }

func setupTestManagerWithCP(t *testing.T, cp ControlPlane) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{
		WorkingDir: dir,
	}, cp, logger, testWorkerFactory("ok"), nil, nil)
	return mgr, dir
}

func TestComputeEffectiveTools_CPIntersection(t *testing.T) {
	cp := &mockCP{
		tools: map[string][]string{
			"restricted": {"Read", "Grep"},
		},
	}
	mgr, dir := setupTestManagerWithCP(t, cp)
	writeAgentMD(t, dir, "restricted", `---
name: restricted
allowed_tools: [Bash, Read, Grep, Write]
---
Restricted agent.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("restricted"))
	effective, _, _, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	// Only Read and Grep survive intersection.
	if effective["Bash"] || effective["Write"] {
		t.Error("Bash and Write should be removed by CP intersection")
	}
	if !effective["Read"] || !effective["Grep"] {
		t.Error("Read and Grep should survive CP intersection")
	}
}

func TestComputeEffectiveTools_CPNoOverride(t *testing.T) {
	cp := &mockCP{tools: map[string][]string{}} // no override for this agent
	mgr, dir := setupTestManagerWithCP(t, cp)
	writeAgentMD(t, dir, "free", `---
name: free
allowed_tools: [Bash, Read, Write]
---
Free agent.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("free"))
	effective, _, _, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	// All tools pass through when CP has no override.
	if !effective["Bash"] || !effective["Read"] || !effective["Write"] {
		t.Errorf("all tools should pass through with no CP override, got %v", effective)
	}
}

func TestComputeEffectiveTools_CPDenyRulesMerged(t *testing.T) {
	cp := &mockCP{
		denyTools: map[string][]string{
			"worker": {"Bash(sudo *)"},
		},
	}
	mgr, dir := setupTestManagerWithCP(t, cp)
	writeAgentMD(t, dir, "worker", `---
name: worker
allowed_tools: [Bash, Read]
disallowed_tools: [Bash(rm *)]
---
Worker.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("worker"))
	_, _, denyRules, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	// Should have 2 deny rules: rm from agent + sudo from CP.
	if len(denyRules) != 2 {
		t.Fatalf("expected 2 deny rules (agent + CP), got %d", len(denyRules))
	}
	patterns := map[string]bool{}
	for _, r := range denyRules {
		patterns[r.Pattern] = true
	}
	if !patterns["rm *"] || !patterns["sudo *"] {
		t.Errorf("expected rm * and sudo * deny patterns, got %v", denyRules)
	}
}

func TestComputeEffectiveTools_CPParameterizedIntersection(t *testing.T) {
	// Agent allows curl and git, CP only allows curl.
	cp := &mockCP{
		tools: map[string][]string{
			"agent": {"Bash(curl *)"},
		},
	}
	mgr, dir := setupTestManagerWithCP(t, cp)
	writeAgentMD(t, dir, "agent", `---
name: agent
allowed_tools: [Bash(curl *), Bash(git *)]
---
Agent.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("agent"))
	effective, allowLayers, _, err := mgr.computeEffectiveTools(cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	// Bash should be in effective set (both mention it).
	if !effective["Bash"] {
		t.Fatal("Bash should be in effective set")
	}

	// Should have 2 allow layers (agent + CP), both with parameterized rules.
	if len(allowLayers) != 2 {
		t.Fatalf("expected 2 allow layers, got %d", len(allowLayers))
	}
}

func TestComputeEffectiveTools_ParentInheritance(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "child", `---
name: child
allowed_tools: [Bash, Read, Write, Grep]
---
Child.`)

	// Simulate a parent with restricted tools.
	parentID := "parent-123"
	mgr.mu.Lock()
	mgr.instances[parentID] = &instance{
		effectiveTools: map[string]bool{"Bash": true, "Read": true},
		allowLayers:    nil,
		denyRules:      nil,
	}
	mgr.mu.Unlock()

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("child"))
	effective, _, _, err := mgr.computeEffectiveTools(cfg, parentID)
	if err != nil {
		t.Fatal(err)
	}

	// Only Bash and Read survive (parent doesn't have Write or Grep).
	if !effective["Bash"] || !effective["Read"] {
		t.Error("Bash and Read should survive parent intersection")
	}
	if effective["Write"] || effective["Grep"] {
		t.Error("Write and Grep should be removed by parent intersection")
	}
}

func TestComputeEffectiveTools_ParentDenyRulesInherited(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "child", `---
name: child
allowed_tools: [Bash, Read]
---
Child.`)

	parentID := "parent-456"
	mgr.mu.Lock()
	mgr.instances[parentID] = &instance{
		effectiveTools: map[string]bool{"Bash": true, "Read": true},
		denyRules:      []toolrules.Rule{{Tool: "Bash", Pattern: "rm *"}},
	}
	mgr.mu.Unlock()

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("child"))
	_, _, denyRules, err := mgr.computeEffectiveTools(cfg, parentID)
	if err != nil {
		t.Fatal(err)
	}

	if len(denyRules) != 1 || denyRules[0].Pattern != "rm *" {
		t.Errorf("expected parent deny rule inherited, got %v", denyRules)
	}
}

func TestComputeEffectiveTools_ParseError(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "bad", `---
name: bad
allowed_tools: ["Bash("]
---
Bad.`)

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("bad"))
	_, _, _, err := mgr.computeEffectiveTools(cfg, "")
	if err == nil {
		t.Error("expected error for malformed rule")
	}
}

func TestBuildAllowedToolsMap_EphemeralMode(t *testing.T) {
	effective := map[string]bool{"Bash": true, "Read": true}
	allowed := buildAllowedToolsMap(effective, config.ModeEphemeral, false)

	// Ephemeral agents get SpawnInstance but NOT management or persistent tools.
	if !allowed["SpawnInstance"] {
		t.Error("ephemeral agents should get SpawnInstance")
	}
	if allowed["ResumeInstance"] || allowed["StopInstance"] || allowed["DeleteInstance"] || allowed["SendMessage"] || allowed["ListInstances"] {
		t.Error("ephemeral agents should not get management tools")
	}
	if allowed["TodoWrite"] {
		t.Error("ephemeral agents should not get persistent tools")
	}
}

func TestBuildAllowedToolsMap_PersistentMode(t *testing.T) {
	effective := map[string]bool{"Bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModePersistent, false)

	// Persistent agents get SpawnInstance + persistent tools, but NOT operator tools.
	if !allowed["SpawnInstance"] {
		t.Error("persistent agents should get SpawnInstance")
	}
	if !allowed["TodoWrite"] || !allowed["HistorySearch"] || !allowed["HistoryRecall"] {
		t.Error("persistent agents should get persistent tools")
	}
	if allowed["CreatePersistentInstance"] || allowed["ResumeInstance"] || allowed["StopInstance"] || allowed["SendMessage"] || allowed["ListInstances"] {
		t.Error("persistent agents should not get management tools unless declared in allowed_tools")
	}
}

func TestBuildAllowedToolsMap_PersistentWithManagementTools(t *testing.T) {
	// When management tools are declared in allowed_tools, they should flow through.
	effective := map[string]bool{"Bash": true, "CreatePersistentInstance": true, "SendMessage": true, "ListInstances": true}
	allowed := buildAllowedToolsMap(effective, config.ModePersistent, false)

	if !allowed["SpawnInstance"] {
		t.Error("should get SpawnInstance")
	}
	if !allowed["CreatePersistentInstance"] || !allowed["SendMessage"] || !allowed["ListInstances"] {
		t.Error("management tools declared in allowed_tools should be in the map")
	}
	if !allowed["TodoWrite"] || !allowed["HistorySearch"] || !allowed["HistoryRecall"] {
		t.Error("should get persistent tools")
	}
}

func TestBuildAllowedToolsMap_WithSkills(t *testing.T) {
	effective := map[string]bool{"Bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModeEphemeral, true)
	if !allowed["Skill"] {
		t.Error("Skill should be included when hasSkills is true")
	}
}

func TestManager_PersistentMode_RestoredOnRestart(t *testing.T) {
	dir := t.TempDir()
	writeAgentMD(t, dir, "coord", `---
name: coord
model: fake-model
---
Operator.`)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Start persistent instance, shut down
	pdb := openTestPDB(t, dir)
	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	id, err := mgr1.CreateInstance(ctx, "coord", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()

	// Restore — persistent mode should survive
	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreInstances(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, ok := mgr2.GetInstance(id)
	if !ok {
		t.Fatal("persistent instance should be restored")
	}
	if info.Mode != config.ModePersistent {
		t.Errorf("restored mode = %q, want persistent", info.Mode)
	}
}

func TestManager_PersistentMode_SessionNotCleaned(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "coord", `---
name: coord
model: fake-model
---
Operator.`)

	id, _ := mgr.CreateInstance(t.Context(), "coord", "", "persistent", "", "", "", "")
	sessDir := filepath.Join(dir, "instances", id)

	mgr.StopInstance(id)

	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		t.Error("persistent session dir should survive stop")
	}
}

func TestManager_ManagementTools_InSpawnConfig(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
allowed_tools: [Bash, DeleteInstance, SendMessage, ListInstances]
---
Operator.`)

	_, err := mgr.CreateInstance(t.Context(), "coord", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Management tools declared in allowed_tools should be in effective tools
	if !cfg.EffectiveTools["DeleteInstance"] {
		t.Error("should have DeleteInstance in effective tools")
	}
	if !cfg.EffectiveTools["SpawnInstance"] {
		t.Error("should have SpawnInstance in effective tools")
	}
	if !cfg.EffectiveTools["TodoWrite"] {
		t.Error("persistent should have TodoWrite in effective tools")
	}
}

func TestManager_Groups_InSpawnConfig(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
model: fake-model
allowed_tools: [Bash]
groups: [hiro-operators]
---
Operator.`)

	_, err := mgr.CreateInstance(t.Context(), "coord", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Should have both primary group and operator group (order-independent).
	groupSet := make(map[uint32]bool)
	for _, g := range cfg.Groups {
		groupSet[g] = true
	}
	if !groupSet[10000] {
		t.Error("primary GID 10000 should be in groups")
	}
	if !groupSet[10001] {
		t.Error("hiro-operators GID 10001 should be in groups")
	}
	if len(cfg.Groups) != 2 {
		t.Errorf("expected 2 groups, got %v", cfg.Groups)
	}
}

func TestManager_Groups_UnknownGroupSkipped(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	// "hiro-operators" not registered in pool
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
model: fake-model
allowed_tools: [Bash]
groups: [hiro-operators]
---
Operator.`)

	_, err := mgr.CreateInstance(t.Context(), "coord", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Only primary group — unknown group silently skipped
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected 1 group (primary only), got %v", cfg.Groups)
	}
	if cfg.Groups[0] != 10000 {
		t.Errorf("group should be primary (10000), got %d", cfg.Groups[0])
	}
}

func TestManager_NoGroups_NoExtraGIDs(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "worker", `---
name: worker
model: fake-model
allowed_tools: [Bash]
---
Worker.`)

	_, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Should only have primary group — no groups declared in frontmatter
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected 1 group, got %v", cfg.Groups)
	}
	if cfg.Groups[0] != 10000 {
		t.Errorf("group should be primary (10000), got %d", cfg.Groups[0])
	}
}

func TestManager_EphemeralAgent_NoManagementTools(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "worker", `---
name: worker
model: fake-model
allowed_tools: [Bash]
---
Worker.`)

	// Start an ephemeral agent directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("worker"))
	mgr.startInstance(t.Context(), "test-eph-id", "test-eph-sess-id", cfg, "", config.ModeEphemeral, "", "", "", "")

	spawnCfg := (*configs)[0]
	if !spawnCfg.EffectiveTools["SpawnInstance"] {
		t.Error("ephemeral agent should have SpawnInstance")
	}
	if spawnCfg.EffectiveTools["TodoWrite"] {
		t.Error("ephemeral should NOT have TodoWrite")
	}
	// Management tools must never leak to agents that don't declare them.
	for _, tool := range []string{"CreatePersistentInstance", "ResumeInstance", "StopInstance", "DeleteInstance", "SendMessage", "ListInstances"} {
		if spawnCfg.EffectiveTools[tool] {
			t.Errorf("ephemeral should NOT have management tool %s", tool)
		}
	}
}

func TestManager_Groups_InheritedFromParent(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	pool.SetGroupGID("custom-group", 20000)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)

	// Parent agent has hiro-operators but NOT custom-group.
	writeAgentMD(t, dir, "parent-agent", `---
name: parent-agent
allowed_tools: [Bash, CreatePersistentInstance]
groups: [hiro-operators]
---
Parent.`)

	// Child agent declares both groups.
	writeAgentMD(t, dir, "child-agent", `---
name: child-agent
allowed_tools: [Bash]
groups: [hiro-operators, custom-group]
---
Child.`)

	parentID, err := mgr.CreateInstance(t.Context(), "parent-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parentCfg := (*configs)[0]

	// Parent should have hiro-operators.
	parentGroupSet := make(map[uint32]bool)
	for _, g := range parentCfg.Groups {
		parentGroupSet[g] = true
	}
	if !parentGroupSet[10001] {
		t.Error("parent should have hiro-operators (10001)")
	}

	// Spawn child with parent.
	_, err = mgr.CreateInstance(t.Context(), "child-agent", parentID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	childCfg := (*configs)[1]

	childGroupSet := make(map[uint32]bool)
	for _, g := range childCfg.Groups {
		childGroupSet[g] = true
	}

	// Child should inherit hiro-operators (parent has it).
	if !childGroupSet[10001] {
		t.Error("child should have hiro-operators (inherited from parent)")
	}

	// Child should NOT get custom-group (parent doesn't have it).
	if childGroupSet[20000] {
		t.Error("child should NOT have custom-group (parent doesn't have it)")
	}
}

func TestManager_Groups_UnprivilegedParentCannotEscalate(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)

	// Parent has no groups.
	writeAgentMD(t, dir, "unpriv", `---
name: unpriv
allowed_tools: [Bash, CreatePersistentInstance]
---
Unprivileged.`)

	// Child declares hiro-operators.
	writeAgentMD(t, dir, "wants-coord", `---
name: wants-coord
allowed_tools: [Bash]
groups: [hiro-operators]
---
Wants escalation.`)

	parentID, err := mgr.CreateInstance(t.Context(), "unpriv", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	_, err = mgr.CreateInstance(t.Context(), "wants-coord", parentID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	childCfg := (*configs)[1]

	// Child should only have the primary group — no hiro-operators.
	if len(childCfg.Groups) != 1 {
		t.Fatalf("expected 1 group (primary only), got %v", childCfg.Groups)
	}
	if childCfg.Groups[0] != 10000 {
		t.Errorf("expected primary GID 10000, got %d", childCfg.Groups[0])
	}
}

func TestManager_Groups_ChildDoesNotAutoInheritParentGroups(t *testing.T) {
	// A privileged parent spawning a child that declares NO groups
	// should NOT pass its groups to the child. Groups are opt-in.
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)

	writeAgentMD(t, dir, "coord", `---
name: coord
allowed_tools: [Bash, CreatePersistentInstance]
groups: [hiro-operators]
---
Operator.`)

	writeAgentMD(t, dir, "plain-worker", `---
name: plain-worker
allowed_tools: [Bash]
---
Worker with no group declarations.`)

	parentID, err := mgr.CreateInstance(t.Context(), "coord", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	_, err = mgr.CreateInstance(t.Context(), "plain-worker", parentID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	childCfg := (*configs)[1]

	// Child should only have primary group — parent's hiro-operators NOT inherited.
	if len(childCfg.Groups) != 1 {
		t.Fatalf("expected 1 group (primary only), got %v", childCfg.Groups)
	}
	if childCfg.Groups[0] != 10000 {
		t.Errorf("expected primary GID 10000, got %d", childCfg.Groups[0])
	}
}

func TestManager_Groups_RootInstanceGetsAllDeclaredGroups(t *testing.T) {
	// Root instances (no parent) get whatever they declare — no intersection.
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	pool.SetGroupGID("custom-group", 20000)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)

	writeAgentMD(t, dir, "root-agent", `---
name: root-agent
allowed_tools: [Bash]
groups: [hiro-operators, custom-group]
---
Root agent with multiple groups.`)

	_, err := mgr.CreateInstance(t.Context(), "root-agent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cfg := (*configs)[0]

	groupSet := make(map[uint32]bool)
	for _, g := range cfg.Groups {
		groupSet[g] = true
	}
	if !groupSet[10000] {
		t.Error("root should have primary GID 10000")
	}
	if !groupSet[10001] {
		t.Error("root should have hiro-operators (10001)")
	}
	if !groupSet[20000] {
		t.Error("root should have custom-group (20000)")
	}
	if len(cfg.Groups) != 3 {
		t.Errorf("expected 3 groups, got %v", cfg.Groups)
	}
}

func TestManager_Groups_ThreeLevelInheritance(t *testing.T) {
	// Grandchild can only get groups that flow through the entire chain.
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	pool.SetGroupGID("extra-group", 20000)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)

	// Root has both groups.
	writeAgentMD(t, dir, "root", `---
name: root
allowed_tools: [Bash, CreatePersistentInstance]
groups: [hiro-operators, extra-group]
---
Root.`)

	// Middle agent only has hiro-operators (not extra-group).
	writeAgentMD(t, dir, "middle", `---
name: middle
allowed_tools: [Bash, CreatePersistentInstance]
groups: [hiro-operators]
---
Middle.`)

	// Leaf requests both groups.
	writeAgentMD(t, dir, "leaf", `---
name: leaf
allowed_tools: [Bash]
groups: [hiro-operators, extra-group]
---
Leaf.`)

	rootID, err := mgr.CreateInstance(t.Context(), "root", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create root: %v", err)
	}

	middleID, err := mgr.CreateInstance(t.Context(), "middle", rootID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create middle: %v", err)
	}

	_, err = mgr.CreateInstance(t.Context(), "leaf", middleID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}

	leafCfg := (*configs)[2]
	leafGroups := make(map[uint32]bool)
	for _, g := range leafCfg.Groups {
		leafGroups[g] = true
	}

	// Leaf should get hiro-operators (root has it, middle has it, leaf declares it).
	if !leafGroups[10001] {
		t.Error("leaf should have hiro-operators")
	}

	// Leaf should NOT get extra-group — middle doesn't have it, breaking the chain.
	if leafGroups[20000] {
		t.Error("leaf should NOT have extra-group (middle doesn't have it)")
	}
}

func TestManager_Groups_EphemeralChildSameRules(t *testing.T) {
	// Ephemeral children follow the same group inheritance rules.
	pool := uidpool.New(10000, 10000, 64)
	pool.SetGroupGID("hiro-operators", 10001)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)

	writeAgentMD(t, dir, "no-groups-parent", `---
name: no-groups-parent
allowed_tools: [Bash]
---
Parent without groups.`)

	writeAgentMD(t, dir, "eph-child", `---
name: eph-child
allowed_tools: [Bash]
groups: [hiro-operators]
---
Ephemeral child wanting groups.`)

	parentID, err := mgr.CreateInstance(t.Context(), "no-groups-parent", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("eph-child"))
	mgr.startInstance(t.Context(), "eph-id", "eph-sess-id", cfg, parentID, config.ModeEphemeral, "", "", "", "")

	childCfg := (*configs)[1]

	// Ephemeral child should only have primary group.
	if len(childCfg.Groups) != 1 {
		t.Fatalf("expected 1 group, got %v", childCfg.Groups)
	}
}

func TestManager_Groups_NoPoolNoGroups(t *testing.T) {
	// Without a UID pool (local dev, no Docker), groups are irrelevant.
	mgr, dir := setupTestManager(t) // no pool
	writeAgentMD(t, dir, "agent-with-groups", `---
name: agent-with-groups
allowed_tools: [Bash]
groups: [hiro-operators]
---
Agent.`)

	// Should succeed — groups are silently ignored without a pool.
	_, err := mgr.CreateInstance(t.Context(), "agent-with-groups", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
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
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: Old desc\nallowed_tools: [Bash, Read]\n---\nWork."), 0o644)

	id, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Update agent.md
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: New desc\nallowed_tools: [Bash, Read, Grep]\n---\nUpdated work."), 0o644)
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
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\n---\nWork."), 0o644)

	// Start and stop a session
	id, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "", "", "", "")
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
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: Old desc\n---\nWork."), 0o644)

	id, err := mgr.CreateInstance(t.Context(), "worker", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Update description
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: New desc\n---\nWork."), 0o644)
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
		os.MkdirAll(agentDir, 0o755)
		os.WriteFile(filepath.Join(agentDir, "agent.md"),
			[]byte("---\nname: "+name+"\ndescription: old\nallowed_tools: [Bash]\n---\nDo stuff."), 0o644)
	}

	idA, err := mgr.CreateInstance(t.Context(), "alpha", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := mgr.CreateInstance(t.Context(), "beta", "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Update descriptions.
	for _, name := range []string{"alpha", "beta"} {
		agentDir := filepath.Join(dir, "agents", name)
		os.WriteFile(filepath.Join(agentDir, "agent.md"),
			[]byte("---\nname: "+name+"\ndescription: updated\nallowed_tools: [Bash]\n---\nDo stuff."), 0o644)
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

func TestListAgentDefs(t *testing.T) {
	mgr, dir := setupTestManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "alpha", "---\nname: alpha\ndescription: First agent.\n---\nPrompt.")
	writeAgentMD(t, dir, "beta", "---\nname: beta\ndescription: Second agent.\n---\nPrompt.")

	defs := mgr.ListAgentDefs()
	if len(defs) != 2 {
		t.Fatalf("expected 2 agent defs, got %d", len(defs))
	}
	// Should be sorted by name.
	if defs[0].Name != "alpha" || defs[1].Name != "beta" {
		t.Errorf("expected sorted [alpha, beta], got [%s, %s]", defs[0].Name, defs[1].Name)
	}
	if defs[0].Description != "First agent." {
		t.Errorf("expected description 'First agent.', got %q", defs[0].Description)
	}
}

func TestListAgentDefs_Empty(t *testing.T) {
	mgr, _ := setupTestManager(t)
	defer mgr.Shutdown()

	defs := mgr.ListAgentDefs()
	if len(defs) != 0 {
		t.Fatalf("expected 0 agent defs, got %d", len(defs))
	}
}

func TestListAgentDefs_SkipsInvalid(t *testing.T) {
	mgr, dir := setupTestManager(t)
	defer mgr.Shutdown()

	writeAgentMD(t, dir, "good", "---\nname: good\ndescription: Valid.\n---\nPrompt.")
	// Write a malformed agent.md (missing name).
	agentDir := filepath.Join(dir, "agents", "bad")
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\ndescription: No name.\n---\nPrompt."), 0o644)

	defs := mgr.ListAgentDefs()
	if len(defs) != 1 {
		t.Fatalf("expected 1 agent def (skipping invalid), got %d", len(defs))
	}
	if defs[0].Name != "good" {
		t.Errorf("expected 'good', got %q", defs[0].Name)
	}
}
