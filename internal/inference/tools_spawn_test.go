package inference

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/ipc"
)

// fakeHostManager implements ipc.HostManager with controllable behavior.
type fakeHostManager struct {
	mu              sync.Mutex
	spawnResult     string
	spawnErr        error
	spawnDelay      time.Duration
	createResult    string
	createErr       error
	lastSpawnAgent  string
	lastSpawnPrompt string
	lastCreateAgent string
	lastCreateMode  string
	agentDefs       []ipc.AgentDef
}

func (f *fakeHostManager) SpawnEphemeral(_ context.Context, agentName, prompt, _ string, _ ipc.NodeID, _ func(ipc.ChatEvent) error) (string, error) {
	f.mu.Lock()
	f.lastSpawnAgent = agentName
	f.lastSpawnPrompt = prompt
	delay := f.spawnDelay
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	return f.spawnResult, f.spawnErr
}

func (f *fakeHostManager) CreateInstance(_ context.Context, name, _, mode string, _ ipc.NodeID, _, _ string) (string, error) {
	f.mu.Lock()
	f.lastCreateAgent = name
	f.lastCreateMode = mode
	f.mu.Unlock()
	return f.createResult, f.createErr
}

func (f *fakeHostManager) SendMessage(context.Context, string, string, func(ipc.ChatEvent) error) (string, error) {
	return "", nil
}
func (f *fakeHostManager) StopInstance(string) (ipc.InstanceInfo, error) {
	return ipc.InstanceInfo{}, nil
}
func (f *fakeHostManager) StartInstance(context.Context, string) error  { return nil }
func (f *fakeHostManager) DeleteInstance(string) error                  { return nil }
func (f *fakeHostManager) NewSession(string) (string, error)            { return "", nil }
func (f *fakeHostManager) IsDescendant(string, string) bool             { return true }
func (f *fakeHostManager) ListChildInstances(string) []ipc.InstanceInfo { return nil }
func (f *fakeHostManager) SecretNames() []string                        { return nil }
func (f *fakeHostManager) SecretEnv() []string                          { return nil }
func (f *fakeHostManager) ListNodes() []ipc.NodeInfo                    { return nil }
func (f *fakeHostManager) ListAgentDefs() []ipc.AgentDef                { return f.agentDefs }

func runTool(t *testing.T, tool fantasy.AgentTool, input string) fantasy.ToolResponse {
	t.Helper()
	ctx := ContextWithCallerID(context.Background(), "test-caller")
	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  tool.Info().Name,
		Input: input,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp
}

// drainWithTimeout blocks on the notification queue's Ready channel until an
// item arrives or the timeout expires.
func drainWithTimeout(t *testing.T, nq *NotificationQueue, timeout time.Duration) []Notification {
	t.Helper()
	select {
	case <-nq.Ready():
		items := nq.Drain()
		if len(items) > 0 {
			return items
		}
	case <-time.After(timeout):
	}
	t.Fatal("timed out waiting for notification")
	return nil
}

// --- SpawnInstance tests ---

func TestSpawnInstance_Sync(t *testing.T) {
	mgr := &fakeHostManager{spawnResult: "done"}
	nq := NewNotificationQueue(testLogger)
	tool := buildSpawnTool(mgr, nq, "session-1", nil, testLogger)

	resp := runTool(t, tool, `{"agent":"test","prompt":"do it"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if resp.Content != "done" {
		t.Errorf("expected 'done', got %q", resp.Content)
	}
	if nq.Len() != 0 {
		t.Error("sync spawn should not push notifications")
	}
}

func TestSpawnInstance_Background(t *testing.T) {
	mgr := &fakeHostManager{
		spawnResult: "background result",
		spawnDelay:  50 * time.Millisecond,
	}
	nq := NewNotificationQueue(testLogger)
	tool := buildSpawnTool(mgr, nq, "session-1", nil, testLogger)

	resp := runTool(t, tool, `{"agent":"test","prompt":"do it","background":true}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if resp.Content == "background result" {
		t.Error("background spawn should not return the result directly")
	}

	items := drainWithTimeout(t, nq, 2*time.Second)
	if len(items) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(items))
	}
	n := items[0]
	if n.Source != "agent-completion" {
		t.Errorf("expected source 'agent-completion', got %q", n.Source)
	}
	if n.SessionID != "session-1" {
		t.Errorf("expected session ID 'session-1', got %q", n.SessionID)
	}
	if !strings.Contains(n.Content, "background result") {
		t.Errorf("notification should contain the result: %s", n.Content)
	}
	if !strings.Contains(n.Content, "completed") {
		t.Errorf("notification should indicate completion: %s", n.Content)
	}
}

func TestSpawnInstance_BackgroundError(t *testing.T) {
	mgr := &fakeHostManager{spawnErr: context.DeadlineExceeded}
	nq := NewNotificationQueue(testLogger)
	tool := buildSpawnTool(mgr, nq, "session-1", nil, testLogger)

	resp := runTool(t, tool, `{"agent":"test","prompt":"do it","background":true}`)
	if resp.IsError {
		t.Fatal("background launch itself should not error")
	}

	items := drainWithTimeout(t, nq, 2*time.Second)
	if len(items) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(items))
	}
	if !strings.Contains(items[0].Content, "failed") {
		t.Errorf("notification should indicate failure: %s", items[0].Content)
	}
}

func TestSpawnInstance_SyncError(t *testing.T) {
	mgr := &fakeHostManager{spawnErr: context.DeadlineExceeded}
	nq := NewNotificationQueue(testLogger)
	tool := buildSpawnTool(mgr, nq, "session-1", nil, testLogger)

	resp := runTool(t, tool, `{"agent":"test","prompt":"do it"}`)
	if !resp.IsError {
		t.Error("sync spawn error should be returned as tool error")
	}
	if !strings.Contains(resp.Content, "deadline") {
		t.Errorf("error should contain cause: %s", resp.Content)
	}
}

func TestSpawnInstance_EmptyAgent(t *testing.T) {
	mgr := &fakeHostManager{}
	nq := NewNotificationQueue(testLogger)
	tool := buildSpawnTool(mgr, nq, "session-1", nil, testLogger)

	resp := runTool(t, tool, `{"agent":"","prompt":"do it"}`)
	if !resp.IsError {
		t.Error("empty agent should be rejected")
	}
}

func TestSpawnInstance_EmptyPrompt(t *testing.T) {
	mgr := &fakeHostManager{}
	nq := NewNotificationQueue(testLogger)
	tool := buildSpawnTool(mgr, nq, "session-1", nil, testLogger)

	resp := runTool(t, tool, `{"agent":"test","prompt":""}`)
	if !resp.IsError {
		t.Error("empty prompt should be rejected")
	}
}

// --- CreatePersistentInstance tests ---

func TestCreatePersistentInstance_Default(t *testing.T) {
	mgr := &fakeHostManager{createResult: "inst-123"}
	tool := buildCreatePersistentInstanceTool(mgr, nil, testLogger)

	resp := runTool(t, tool, `{"agent":"researcher"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "inst-123") {
		t.Errorf("response should contain instance ID: %s", resp.Content)
	}
	mgr.mu.Lock()
	if mgr.lastCreateMode != "persistent" {
		t.Errorf("expected default mode 'persistent', got %q", mgr.lastCreateMode)
	}
	mgr.mu.Unlock()
}

func TestCreatePersistentInstance_AlwaysPersistent(t *testing.T) {
	mgr := &fakeHostManager{createResult: "inst-456"}
	tool := buildCreatePersistentInstanceTool(mgr, nil, testLogger)

	resp := runTool(t, tool, `{"agent":"manager"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	mgr.mu.Lock()
	if mgr.lastCreateMode != "persistent" {
		t.Errorf("expected mode 'persistent', got %q", mgr.lastCreateMode)
	}
	mgr.mu.Unlock()
}

func TestCreatePersistentInstance_EmptyAgent(t *testing.T) {
	mgr := &fakeHostManager{}
	tool := buildCreatePersistentInstanceTool(mgr, nil, testLogger)

	resp := runTool(t, tool, `{"agent":""}`)
	if !resp.IsError {
		t.Error("empty agent should be rejected")
	}
}

func TestBuildAgentListingContext_WithAgents(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "General-purpose agent."},
			{Name: "critic", Description: "Review agent."},
		},
	}
	ctx := buildAgentListingContext(mgr)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	if ctx.Heading != "Agents" {
		t.Errorf("expected heading 'Agents', got %q", ctx.Heading)
	}
	if !strings.Contains(ctx.Content, "**assistant**") {
		t.Error("expected assistant in listing")
	}
	if !strings.Contains(ctx.Content, "**critic**") {
		t.Error("expected critic in listing")
	}
}

func TestBuildAgentListingContext_NoAgents(t *testing.T) {
	mgr := &fakeHostManager{}
	ctx := buildAgentListingContext(mgr)
	if ctx != nil {
		t.Error("expected nil context when no agents defined")
	}
}

func TestSpawnTool_CarriesContext(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "expert", Description: "Deep investigator."},
		},
	}
	agentCtx := buildAgentListingContext(mgr)
	tool := buildSpawnTool(mgr, NewNotificationQueue(testLogger), "session-1", agentCtx, testLogger)
	if tool.Context == nil {
		t.Fatal("SpawnInstance tool should carry agent listing context")
	}
	if tool.Context.Heading != "Agents" {
		t.Errorf("expected heading 'Agents', got %q", tool.Context.Heading)
	}
}

func TestCreatePersistentInstanceTool_CarriesContext(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "General-purpose agent."},
		},
	}
	agentCtx := buildAgentListingContext(mgr)
	tool := buildCreatePersistentInstanceTool(mgr, agentCtx, testLogger)
	if tool.Context == nil {
		t.Fatal("CreatePersistentInstance tool should carry agent listing context")
	}
	if tool.Context != agentCtx {
		t.Error("both spawn tools should share the same context pointer")
	}
}
