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

func (f *fakeHostManager) CreateInstance(_ context.Context, name, _, mode string, _ ipc.NodeID, _, _, _ string) (string, error) {
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
	tool := buildSpawnTool(mgr, nq, "session-1", testLogger)

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
	tool := buildSpawnTool(mgr, nq, "session-1", testLogger)

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
	tool := buildSpawnTool(mgr, nq, "session-1", testLogger)

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
	tool := buildSpawnTool(mgr, nq, "session-1", testLogger)

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
	tool := buildSpawnTool(mgr, nq, "session-1", testLogger)

	resp := runTool(t, tool, `{"agent":"","prompt":"do it"}`)
	if !resp.IsError {
		t.Error("empty agent should be rejected")
	}
}

func TestSpawnInstance_EmptyPrompt(t *testing.T) {
	mgr := &fakeHostManager{}
	nq := NewNotificationQueue(testLogger)
	tool := buildSpawnTool(mgr, nq, "session-1", testLogger)

	resp := runTool(t, tool, `{"agent":"test","prompt":""}`)
	if !resp.IsError {
		t.Error("empty prompt should be rejected")
	}
}

// --- CreatePersistentInstance tests ---

func TestCreatePersistentInstance_Default(t *testing.T) {
	mgr := &fakeHostManager{createResult: "inst-123"}
	tool := buildCreatePersistentInstanceTool(mgr, testLogger)

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
	tool := buildCreatePersistentInstanceTool(mgr, testLogger)

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
	tool := buildCreatePersistentInstanceTool(mgr, testLogger)

	resp := runTool(t, tool, `{"agent":""}`)
	if !resp.IsError {
		t.Error("empty agent should be rejected")
	}
}

func TestAgentListingProvider_InitialAnnouncement(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "General-purpose agent."},
			{Name: "critic", Description: "Review agent."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}

	// No prior history → full announcement.
	dr := provider(tools, nil)
	if dr == nil {
		t.Fatal("expected initial delta result")
	}
	text := textPartText(t, dr.Message.Content[0])
	if !strings.Contains(text, "**assistant**") {
		t.Error("expected assistant in listing")
	}
	if !strings.Contains(text, "**critic**") {
		t.Error("expected critic in listing")
	}
	if !strings.Contains(text, "<system-reminder>") {
		t.Error("expected system-reminder wrapper")
	}
}

func TestAgentListingProvider_NoChangeNoMessage(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}

	// First call: initial announcement.
	dr := provider(tools, nil)
	if dr == nil {
		t.Fatal("expected initial announcement")
	}

	// Second call with prior delta in history: nothing changed.
	history := []fantasy.Message{dr.Message}
	dr2 := provider(tools, history)
	if dr2 != nil {
		t.Error("expected nil when nothing changed (cache should be preserved)")
	}
}

func TestAgentListingProvider_DeltaOnAdd(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}

	// Initial announcement.
	dr := provider(tools, nil)
	history := []fantasy.Message{dr.Message}

	// Add an agent at runtime.
	mgr.agentDefs = append(mgr.agentDefs, ipc.AgentDef{Name: "expert", Description: "Investigator."})

	dr2 := provider(tools, history)
	if dr2 == nil {
		t.Fatal("expected delta for new agent")
	}
	text := textPartText(t, dr2.Message.Content[0])
	if !strings.Contains(text, "expert") {
		t.Error("expected expert in delta")
	}
	// Should be a delta, not a full re-announcement.
	if strings.Contains(text, "assistant") {
		t.Error("delta should only mention new agents, not existing ones")
	}
}

func TestAgentListingProvider_DeltaOnRemove(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
			{Name: "expert", Description: "Investigator."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}

	dr := provider(tools, nil)
	history := []fantasy.Message{dr.Message}

	// Remove expert.
	mgr.agentDefs = mgr.agentDefs[:1]

	dr2 := provider(tools, history)
	if dr2 == nil {
		t.Fatal("expected delta for removed agent")
	}
	text := textPartText(t, dr2.Message.Content[0])
	if !strings.Contains(text, "no longer available") {
		t.Error("expected removal notice")
	}
	if !strings.Contains(text, "expert") {
		t.Error("expected expert in removal list")
	}
}

func TestAgentListingProvider_CompactionResets(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}

	// Initial announcement.
	dr := provider(tools, nil)
	if dr == nil {
		t.Fatal("expected initial announcement")
	}

	// Simulate compaction: history is now empty (deltas removed).
	dr2 := provider(tools, nil)
	if dr2 == nil {
		t.Fatal("expected full re-announcement after compaction")
	}
}

func TestAgentListingProvider_NoSpawnTool(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{"Read": true, "Bash": true}
	if dr := provider(tools, nil); dr != nil {
		t.Error("expected nil when SpawnInstance not in active tools")
	}
}

func TestAgentListingProvider_NoAgents(t *testing.T) {
	mgr := &fakeHostManager{}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}
	if dr := provider(tools, nil); dr != nil {
		t.Error("expected nil when no agents defined")
	}
}

func TestAgentListingProvider_SimultaneousAddAndRemove(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
			{Name: "expert", Description: "Investigator."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}

	dr := provider(tools, nil)
	history := []fantasy.Message{dr.Message}

	// Replace expert with critic (remove + add in same turn).
	mgr.agentDefs = []ipc.AgentDef{
		{Name: "assistant", Description: "Helper."},
		{Name: "critic", Description: "Reviewer."},
	}
	dr2 := provider(tools, history)
	if dr2 == nil {
		t.Fatal("expected delta for add+remove")
	}
	text := textPartText(t, dr2.Message.Content[0])
	if !strings.Contains(text, "critic") {
		t.Error("expected critic in new agents")
	}
	if !strings.Contains(text, "expert") {
		t.Error("expected expert in removed agents")
	}
}

func TestAgentListingProvider_AllAgentsRemoved(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
		},
	}
	provider := AgentListingProvider(mgr)
	tools := map[string]bool{SpawnToolName: true}

	dr := provider(tools, nil)
	history := []fantasy.Message{dr.Message}

	// All agents removed.
	mgr.agentDefs = nil
	dr2 := provider(tools, history)
	if dr2 == nil {
		t.Fatal("expected delta for removal")
	}
	text := textPartText(t, dr2.Message.Content[0])
	if !strings.Contains(text, "no longer available") {
		t.Error("expected removal notice")
	}
}

func TestAgentListingProvider_CreatePersistentToolOnly(t *testing.T) {
	mgr := &fakeHostManager{
		agentDefs: []ipc.AgentDef{
			{Name: "assistant", Description: "Helper."},
		},
	}
	provider := AgentListingProvider(mgr)
	// Only CreatePersistentInstance, no SpawnInstance.
	tools := map[string]bool{CreatePersistentToolName: true}
	dr := provider(tools, nil)
	if dr == nil {
		t.Error("expected announcement when CreatePersistentInstance is available")
	}
}

func TestRenderAgentListing_InitialSorted(t *testing.T) {
	defs := []ipc.AgentDef{
		{Name: "critic", Description: "Reviewer."},
		{Name: "assistant", Description: "Helper."},
	}
	text := renderAgentListing(defs, []string{"assistant", "critic"}, nil, true)
	// Defs order should be preserved (they come pre-sorted from ListAgentDefs).
	criticIdx := strings.Index(text, "critic")
	assistantIdx := strings.Index(text, "assistant")
	if criticIdx < 0 || assistantIdx < 0 {
		t.Fatal("expected both agents in listing")
	}
	if criticIdx > assistantIdx {
		t.Error("expected critic before assistant (definition order)")
	}
}

func TestRenderAgentListing_DeltaShowsOnlyChanges(t *testing.T) {
	defs := []ipc.AgentDef{
		{Name: "assistant", Description: "Helper."},
		{Name: "expert", Description: "Investigator."},
	}
	text := renderAgentListing(defs, []string{"expert"}, []string{"critic"}, false)
	if !strings.Contains(text, "expert") {
		t.Error("expected expert in new agents")
	}
	if strings.Contains(text, "assistant") {
		t.Error("should not mention existing agents in delta")
	}
	if !strings.Contains(text, "critic") {
		t.Error("expected critic in removed list")
	}
}

// --- NodeListingProvider tests ---

func TestNodeListingProvider_GatedOnListNodes(t *testing.T) {
	mgr := &controllableFakeManager{
		nodes: []ipc.NodeInfo{{ID: "n1", Name: "worker-1", Status: "online"}},
	}
	provider := NodeListingProvider(mgr)

	// Without ListNodes in active tools, should return nil.
	result := provider(map[string]bool{"Bash": true}, nil)
	if result != nil {
		t.Error("expected nil when ListNodes not active")
	}

	// With ListNodes, should emit.
	result = provider(map[string]bool{"ListNodes": true}, nil)
	if result == nil {
		t.Fatal("expected non-nil result when ListNodes is active")
	}
}

func TestNodeListingProvider_NilWhenNoNodes(t *testing.T) {
	mgr := &controllableFakeManager{nodes: nil}
	provider := NodeListingProvider(mgr)

	result := provider(map[string]bool{"ListNodes": true}, nil)
	if result != nil {
		t.Error("expected nil when no nodes")
	}
}

func TestNodeListingProvider_SuppressesDuplicate(t *testing.T) {
	mgr := &controllableFakeManager{
		nodes: []ipc.NodeInfo{{ID: "n1", Name: "worker-1", Status: "online"}},
	}
	provider := NodeListingProvider(mgr)
	active := map[string]bool{"ListNodes": true}

	// First call emits.
	first := provider(active, nil)
	if first == nil {
		t.Fatal("expected first call to emit")
	}

	// Second call with same history should suppress.
	history := []fantasy.Message{first.Message}
	second := provider(active, history)
	if second != nil {
		t.Error("expected nil on unchanged nodes")
	}
}

func TestNodeListingProvider_EmitsOnChange(t *testing.T) {
	mgr := &controllableFakeManager{
		nodes: []ipc.NodeInfo{{ID: "n1", Name: "worker-1", Status: "online", ActiveCount: 0}},
	}
	provider := NodeListingProvider(mgr)
	active := map[string]bool{"ListNodes": true}

	first := provider(active, nil)
	if first == nil {
		t.Fatal("expected first call to emit")
	}

	// Change node state.
	mgr.nodes[0].ActiveCount = 3
	history := []fantasy.Message{first.Message}
	second := provider(active, history)
	if second == nil {
		t.Error("expected re-emission after node state change")
	}
}

func TestRenderNodeListing_IncludesIDAndStatus(t *testing.T) {
	nodes := []ipc.NodeInfo{
		{ID: "abc123", Name: "leader", Status: "online", IsHome: true, Capacity: 4, ActiveCount: 2},
		{ID: "def456", Name: "worker-1", Status: "online", ActiveCount: 1},
	}
	text := renderNodeListing(nodes)
	if !strings.Contains(text, "leader (home)") {
		t.Error("expected home label")
	}
	if !strings.Contains(text, "id: abc123") {
		t.Error("expected node ID")
	}
	if !strings.Contains(text, "capacity: 4") {
		t.Error("expected capacity for node with capacity > 0")
	}
	// worker-1 has zero capacity — check that its line doesn't include "capacity"
	if idx := strings.Index(text, "def456"); idx >= 0 {
		// Extract the line containing def456.
		end := strings.Index(text[idx:], "\n")
		if end < 0 {
			end = len(text) - idx
		}
		line := text[idx : idx+end]
		if strings.Contains(line, "capacity") {
			t.Error("should not show capacity for zero-capacity node")
		}
	}
}
