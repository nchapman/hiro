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

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
	"github.com/nchapman/hivebot/internal/uidpool"
)

// openTestPDB opens a platform DB in the given directory for testing.
func openTestPDB(t *testing.T, dir string) *platformdb.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0755); err != nil {
		t.Fatal(err)
	}
	pdb, err := platformdb.Open(filepath.Join(dir, "db", "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Close() })
	return pdb
}

// testWorker implements ipc.AgentWorker for testing.
type testWorker struct {
	response         string
	shutdown         bool
	done             chan struct{}
	closed           bool
	lastConfigUpdate *ipc.ConfigUpdate
}

func (w *testWorker) Chat(_ context.Context, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	if onEvent != nil {
		onEvent(ipc.ChatEvent{Type: "delta", Content: w.response})
	}
	return w.response, nil
}

func (w *testWorker) Shutdown(_ context.Context) error {
	w.shutdown = true
	w.closeDone()
	return nil
}

func (w *testWorker) ConfigChanged(_ context.Context, update ipc.ConfigUpdate) error {
	w.lastConfigUpdate = &update
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
	}, nil, logger, "", testWorkerFactory("hello from agent"), nil, nil)
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
	agents := mgr.ListSessions()
	if len(agents) != 0 {
		t.Errorf("new manager should have 0 agents, got %d", len(agents))
	}
}

func TestManager_CreateSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty agent ID")
	}

	info, ok := mgr.GetSession(id)
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
	_, err := mgr.CreateSession(t.Context(), "nonexistent", "", "persistent")
	if err == nil {
		t.Fatal("expected error for missing agent config")
	}
}

func TestManager_CreateSession_InvalidMode(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)
	for _, mode := range []string{"", "supercoordinator", "persistant"} {
		_, err := mgr.CreateSession(t.Context(), "test-agent", "", mode)
		if err == nil {
			t.Errorf("mode %q: expected error, got nil", mode)
		}
	}
}

func TestManager_SendMessage(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
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

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	var events []ipc.ChatEvent
	_, err = mgr.SendMessage(t.Context(), id, "hi", func(evt ipc.ChatEvent) error {
		events = append(events, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected at least one event callback")
	}
}

func TestManager_StopSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	info, err := mgr.StopSession(id)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if info.Name != "test-agent" {
		t.Errorf("stopped name = %q, want test-agent", info.Name)
	}

	// Persistent agents stay in registry with "stopped" status.
	ai, ok := mgr.GetSession(id)
	if !ok {
		t.Fatal("persistent agent should still be in registry after stop")
	}
	if ai.Status != SessionStatusStopped {
		t.Errorf("status = %q, want %q", ai.Status, SessionStatusStopped)
	}
}

func TestManager_StopSession_Ephemeral(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "eph-agent", `---
name: eph-agent
model: fake-model
---
Ephemeral agent.`)

	id, err := mgr.CreateSession(t.Context(), "eph-agent", "", "ephemeral")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	mgr.StopSession(id)

	// Ephemeral agents are fully removed after stop.
	if _, ok := mgr.GetSession(id); ok {
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

	parentID, _ := mgr.CreateSession(t.Context(), "parent", "", "persistent")
	childID, _ := mgr.CreateSession(t.Context(), "child", parentID, "persistent")

	// Stopping parent should also stop child (both persistent → soft-stopped).
	mgr.StopSession(parentID)

	parentInfo, ok := mgr.GetSession(parentID)
	if !ok {
		t.Fatal("parent should still be in registry")
	}
	if parentInfo.Status != SessionStatusStopped {
		t.Errorf("parent status = %q, want stopped", parentInfo.Status)
	}

	childInfo, ok := mgr.GetSession(childID)
	if !ok {
		t.Fatal("child should still be in registry")
	}
	if childInfo.Status != SessionStatusStopped {
		t.Errorf("child status = %q, want stopped", childInfo.Status)
	}
}

func TestManager_DeleteSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := mgr.DeleteSession(id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, ok := mgr.GetSession(id); ok {
		t.Error("agent should not exist after delete")
	}
}

func TestManager_StartSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	mgr.StopSession(id)
	ai, _ := mgr.GetSession(id)
	if ai.Status != SessionStatusStopped {
		t.Fatalf("status after stop = %q, want stopped", ai.Status)
	}

	if err := mgr.StartSession(t.Context(), id); err != nil {
		t.Fatalf("restart: %v", err)
	}

	ai, ok := mgr.GetSession(id)
	if !ok {
		t.Fatal("agent should exist after restart")
	}
	if ai.Status != SessionStatusRunning {
		t.Errorf("status after restart = %q, want running", ai.Status)
	}
}

func TestManager_StopSession_AlreadyStopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	mgr.StopSession(id)

	// Stopping an already-stopped agent should be a no-op.
	info, err := mgr.StopSession(id)
	if err != nil {
		t.Fatalf("stop already-stopped: %v", err)
	}
	if info.Status != string(SessionStatusStopped) {
		t.Errorf("status = %q, want stopped", info.Status)
	}
}

func TestManager_StopSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, err := mgr.StopSession("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for stopping nonexistent agent")
	}
}

func TestManager_DeleteSession_Stopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	mgr.StopSession(id)

	if err := mgr.DeleteSession(id); err != nil {
		t.Fatalf("delete stopped agent: %v", err)
	}
	if _, ok := mgr.GetSession(id); ok {
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

	parentID, _ := mgr.CreateSession(t.Context(), "parent", "", "persistent")
	childID, _ := mgr.CreateSession(t.Context(), "child", parentID, "persistent")

	if err := mgr.DeleteSession(parentID); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if _, ok := mgr.GetSession(parentID); ok {
		t.Error("parent should be deleted")
	}
	if _, ok := mgr.GetSession(childID); ok {
		t.Error("child should be deleted with parent")
	}
}

func TestManager_DeleteSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.DeleteSession("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for deleting nonexistent agent")
	}
}

func TestManager_StartSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.StartSession(t.Context(), "nonexistent-id")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestManager_StartSession_AlreadyRunning(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")

	err := mgr.StartSession(t.Context(), id)
	if !errors.Is(err, ErrSessionNotStopped) {
		t.Fatalf("expected ErrSessionNotStopped, got %v", err)
	}
}

func TestManager_StartSession_ErrorRecovery(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	mgr.StopSession(id)

	// Delete the agent definition so startSession will fail on config load.
	os.RemoveAll(mgr.agentDefDir("test-agent"))

	err := mgr.StartSession(t.Context(), id)
	if err == nil {
		t.Fatal("expected error when agent definition is missing")
	}

	// Session should still be visible as stopped (not lost from registry).
	info, ok := mgr.GetSession(id)
	if !ok {
		t.Fatal("session should still be in registry after failed restart")
	}
	if info.Status != SessionStatusStopped {
		t.Errorf("status = %q, want stopped", info.Status)
	}
}

func TestManager_SendMessage_Stopped(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	mgr.StopSession(id)

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

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")

	found, ok := mgr.SessionByAgentName("test-agent")
	if !ok {
		t.Fatal("expected to find agent by name")
	}
	if found != id {
		t.Errorf("found ID = %q, want %q", found, id)
	}
}

func TestManager_SessionByAgentName_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, found := mgr.SessionByAgentName("nope")
	if found {
		t.Error("expected not found")
	}
}

func TestManager_GetSession_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)
	_, found := mgr.GetSession("nonexistent-id")
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

	mgr.CreateSession(t.Context(), "a1", "", "persistent")
	mgr.CreateSession(t.Context(), "a2", "", "persistent")

	agents := mgr.ListSessions()
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

	mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	mgr.Shutdown()

	if len(mgr.ListSessions()) != 0 {
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

	rootID, _ := mgr.CreateSession(t.Context(), "root", "", "persistent")
	childID, _ := mgr.CreateSession(t.Context(), "child", rootID, "persistent")

	info, _ := mgr.GetSession(childID)
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

	rootID, _ := mgr.CreateSession(t.Context(), "root", "", "persistent")
	childID, _ := mgr.CreateSession(t.Context(), "child", rootID, "persistent")

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

	parentID, _ := mgr.CreateSession(t.Context(), "parent", "", "persistent")
	mgr.CreateSession(t.Context(), "child", parentID, "persistent")

	// ListChildren scoped to parent
	children := mgr.ListChildSessions(parentID)
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if children[0].Name != "child" {
		t.Errorf("child name = %q, want child", children[0].Name)
	}

	// ListChildren for agent with no children
	noKids := mgr.ListChildSessions("nonexistent")
	if len(noKids) != 0 {
		t.Errorf("expected 0 children, got %d", len(noKids))
	}
}

func TestManager_SessionDirCreated(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	sessDir := filepath.Join(dir, "sessions", id)
	if _, err := os.Stat(sessDir); err != nil {
		t.Fatalf("session dir should exist at %s: %v", sessDir, err)
	}
}

func TestManager_SessionSubdirs(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
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
	parentID, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")

	// Start an ephemeral child directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("test-agent"))
	ephID, _ := mgr.startSession(t.Context(), "ephemeral-test-id", cfg, parentID, config.ModeEphemeral)

	sessDir := filepath.Join(dir, "sessions", ephID)
	if _, err := os.Stat(sessDir); err != nil {
		t.Fatalf("ephemeral session dir should exist: %v", err)
	}

	mgr.StopSession(ephID)

	if _, err := os.Stat(sessDir); !os.IsNotExist(err) {
		t.Error("ephemeral session dir should be cleaned up after stop")
	}
}

func TestManager_PersistentNotCleaned(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	sessDir := filepath.Join(dir, "sessions", id)

	mgr.StopSession(id)

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
	}, nil, logger, "", testWorkerFactory("hello"), nil, pdb)

	id, err := mgr1.CreateSession(ctx, "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()

	// Create a new manager and restore
	mgr2 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreSessions(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The agent should be running with the same ID
	info, ok := mgr2.GetSession(id)
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
	}, nil, logger, "", testWorkerFactory("hello"), nil, pdb)

	id, err := mgr1.CreateSession(ctx, "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.StopSession(id)
	mgr1.Shutdown()

	// Create a new manager and restore.
	mgr2 := NewManager(ctx, dir, Options{
		WorkingDir: dir,
	}, nil, logger, "", testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreSessions(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The agent should be restored as stopped, not running.
	info, ok := mgr2.GetSession(id)
	if !ok {
		t.Fatal("stopped agent not found after restore")
	}
	if info.Status != SessionStatusStopped {
		t.Errorf("restored status = %q, want stopped", info.Status)
	}

	// Restarting it should work and clear the stopped state.
	if err := mgr2.StartSession(ctx, id); err != nil {
		t.Fatalf("restart: %v", err)
	}
	info, _ = mgr2.GetSession(id)
	if info.Status != SessionStatusRunning {
		t.Errorf("status after restart = %q, want running", info.Status)
	}
}

func TestManager_SpawnSession(t *testing.T) {
	mgr, dir := setupTestManager(t)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	result, err := mgr.SpawnSession(t.Context(), "test-agent", "do something", "", nil)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if result != "hello from agent" {
		t.Errorf("result = %q, want %q", result, "hello from agent")
	}

	// Ephemeral agent should be cleaned up
	agents := mgr.ListSessions()
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
	}, nil, logger, "", factory, pool, nil)
	return mgr, dir, configs
}

func TestManager_UIDPool_Assigned(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
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

	mgr.CreateSession(t.Context(), "a", "", "persistent")
	mgr.CreateSession(t.Context(), "b", "", "persistent")

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

	id, _ := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if pool.InUse() != 1 {
		t.Fatalf("expected 1 UID in use, got %d", pool.InUse())
	}

	mgr.StopSession(id)
	if pool.InUse() != 0 {
		t.Fatalf("expected 0 UIDs in use after stop, got %d", pool.InUse())
	}
}

func TestManager_UIDPool_ReleasedOnShutdown(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, _ := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
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
	}, nil, logger, "", failingWorkerFactory(), pool, nil)
	writeAgentMD(t, dir, "test-agent", testAgentMD)

	_, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
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

	_, err := mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}
	_, err = mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	_, err = mgr.CreateSession(t.Context(), "test-agent", "", "persistent")
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

	parentID, _ := mgr.CreateSession(t.Context(), "parent", "", "persistent")
	mgr.CreateSession(t.Context(), "child", parentID, "persistent")
	if pool.InUse() != 2 {
		t.Fatalf("expected 2 UIDs, got %d", pool.InUse())
	}

	// Stop parent — should release both parent and child UIDs
	mgr.StopSession(parentID)
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
	}, nil, logger, "", factory1, pool, pdb)

	id, err := mgr1.CreateSession(ctx, "test-agent", "", "persistent")
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
	}, nil, logger, "", factory2, pool2, pdb)
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
	info, ok := mgr2.GetSession(id)
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

	_, err := mgr.SpawnSession(t.Context(), "test-agent", "do something", "", nil)
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
	if !allowed["spawn_session"] {
		t.Error("ephemeral agents should get spawn_session")
	}
	if allowed["create_session"] || allowed["resume_session"] || allowed["stop_session"] || allowed["delete_session"] || allowed["send_message"] || allowed["list_sessions"] {
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
	if !allowed["spawn_session"] {
		t.Error("persistent agents should get spawn_session")
	}
	if !allowed["memory_read"] || !allowed["memory_write"] || !allowed["todos"] ||
		!allowed["history_search"] || !allowed["history_recall"] {
		t.Error("persistent agents should get persistent tools")
	}
	if allowed["resume_session"] || allowed["stop_session"] || allowed["send_message"] || allowed["list_sessions"] {
		t.Error("persistent agents should not get coordinator tools")
	}
}

func TestBuildAllowedToolsMap_CoordinatorMode(t *testing.T) {
	effective := map[string]bool{"bash": true}
	allowed := buildAllowedToolsMap(effective, config.ModeCoordinator, false)

	// Coordinator agents get everything: spawn + coordinator + persistent tools.
	if !allowed["spawn_session"] {
		t.Error("coordinators should get spawn_session")
	}
	if !allowed["resume_session"] || !allowed["stop_session"] || !allowed["delete_session"] || !allowed["send_message"] || !allowed["list_sessions"] {
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
model: fake-model
---
Coordinator.`)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := t.Context()

	// Start coordinator, shut down
	pdb := openTestPDB(t, dir)
	mgr1 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, "", testWorkerFactory("hello"), nil, pdb)
	id, err := mgr1.CreateSession(ctx, "coord", "", "coordinator")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mgr1.Shutdown()

	// Restore — coordinator mode should survive (it's persistent)
	mgr2 := NewManager(ctx, dir, Options{WorkingDir: dir}, nil, logger, "", testWorkerFactory("hello"), nil, pdb)
	if err := mgr2.RestoreSessions(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, ok := mgr2.GetSession(id)
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

	id, _ := mgr.CreateSession(t.Context(), "coord", "", "coordinator")
	sessDir := filepath.Join(dir, "sessions", id)

	mgr.StopSession(id)

	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		t.Error("coordinator session dir should survive stop (like persistent)")
	}
}

func TestManager_CoordinatorTools_InSpawnConfig(t *testing.T) {
	pool := uidpool.New(10000, 10000, 64)
	mgr, dir, configs := setupTestManagerWithPool(t, pool)
	writeAgentMD(t, dir, "coord", `---
name: coord
model: fake-model
tools: [bash]
---
Coordinator.`)

	_, err := mgr.CreateSession(t.Context(), "coord", "", "coordinator")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	cfg := (*configs)[0]
	// Coordinator should have coordinator tools in effective tools
	if !cfg.EffectiveTools["delete_session"] {
		t.Error("coordinator should have delete_session in effective tools")
	}
	if !cfg.EffectiveTools["spawn_session"] {
		t.Error("coordinator should have spawn_session in effective tools")
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
model: fake-model
tools: [bash]
---
Coordinator.`)

	_, err := mgr.CreateSession(t.Context(), "coord", "", "coordinator")
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
tools: [bash]
---
Coordinator.`)

	_, err := mgr.CreateSession(t.Context(), "coord", "", "coordinator")
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
tools: [bash]
---
Worker.`)

	_, err := mgr.CreateSession(t.Context(), "worker", "", "persistent")
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
tools: [bash]
---
Worker.`)

	// Start an ephemeral agent directly
	cfg, _ := config.LoadAgentDir(mgr.agentDefDir("worker"))
	mgr.startSession(t.Context(), "test-eph-id", cfg, "", config.ModeEphemeral)

	spawnCfg := (*configs)[0]
	if !spawnCfg.EffectiveTools["spawn_session"] {
		t.Error("ephemeral agent should have spawn_session")
	}
	if spawnCfg.EffectiveTools["memory_read"] {
		t.Error("ephemeral should NOT have memory_read")
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
		{"agents/bar/soul.md", "bar"},
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

	// Write agent definition
	agentDir := filepath.Join(dir, "agents", "worker")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\nmodel: claude-sonnet-4-20250514\ntools: [bash, read_file]\n---\nWork."), 0644)

	// Start a session
	id, err := mgr.CreateSession(t.Context(), "worker", "", "persistent")
	if err != nil {
		t.Fatal(err)
	}

	// Update agent.md with new model
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\nmodel: claude-opus-4-20250514\ntools: [bash, read_file, grep]\n---\nUpdated work."), 0644)

	// Push config update
	mgr.pushConfigUpdate("worker")

	// Verify the worker received the update
	mgr.mu.RLock()
	s := mgr.sessions[id]
	w := s.worker.(*testWorker)
	mgr.mu.RUnlock()

	if w.lastConfigUpdate == nil {
		t.Fatal("expected config update to be pushed to worker")
	}
	if w.lastConfigUpdate.Model != "claude-opus-4-20250514" {
		t.Errorf("model = %q, want %q", w.lastConfigUpdate.Model, "claude-opus-4-20250514")
	}
}

func TestManager_PushConfigUpdate_SkipsStopped(t *testing.T) {
	mgr, dir := setupTestManager(t)

	// Write agent definition
	agentDir := filepath.Join(dir, "agents", "worker")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\n---\nWork."), 0644)

	// Start and stop a session
	id, err := mgr.CreateSession(t.Context(), "worker", "", "persistent")
	if err != nil {
		t.Fatal(err)
	}
	mgr.StopSession(id)

	// Push — should not crash or send to stopped agent
	mgr.pushConfigUpdate("worker")

	// Verify no update was sent (worker is nil after stop)
	mgr.mu.RLock()
	s := mgr.sessions[id]
	mgr.mu.RUnlock()

	if s.info.Status != SessionStatusStopped {
		t.Error("expected session to be stopped")
	}
}

func TestManager_PushConfigUpdate_UpdatesDescription(t *testing.T) {
	mgr, dir := setupTestManager(t)

	agentDir := filepath.Join(dir, "agents", "worker")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: Old desc\n---\nWork."), 0644)

	id, err := mgr.CreateSession(t.Context(), "worker", "", "persistent")
	if err != nil {
		t.Fatal(err)
	}

	// Update description
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: worker\ndescription: New desc\n---\nWork."), 0644)
	mgr.pushConfigUpdate("worker")

	info, ok := mgr.GetSession(id)
	if !ok {
		t.Fatal("session not found")
	}
	if info.Description != "New desc" {
		t.Errorf("description = %q, want %q", info.Description, "New desc")
	}
}

func TestManager_PushConfigUpdateAll(t *testing.T) {
	mgr, dir := setupTestManager(t)

	// Create two different agents.
	for _, name := range []string{"alpha", "beta"} {
		agentDir := filepath.Join(dir, "agents", name)
		os.MkdirAll(agentDir, 0755)
		os.WriteFile(filepath.Join(agentDir, "agent.md"),
			[]byte("---\nname: "+name+"\ntools: [bash]\n---\nDo stuff."), 0644)
	}

	idA, err := mgr.CreateSession(t.Context(), "alpha", "", "persistent")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := mgr.CreateSession(t.Context(), "beta", "", "persistent")
	if err != nil {
		t.Fatal(err)
	}

	// Push to all.
	mgr.PushConfigUpdateAll()

	// Both workers should have received an update.
	mgr.mu.RLock()
	wA := mgr.sessions[idA].worker.(*testWorker)
	wB := mgr.sessions[idB].worker.(*testWorker)
	mgr.mu.RUnlock()

	if wA.lastConfigUpdate == nil {
		t.Error("alpha worker did not receive config update")
	}
	if wB.lastConfigUpdate == nil {
		t.Error("beta worker did not receive config update")
	}
}
