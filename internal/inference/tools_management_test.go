package inference

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/ipc"
)

// controllableFakeManager extends fakeHostManager with controllable behavior
// for management tool tests.
type controllableFakeManager struct {
	fakeHostManager

	// descendant control
	isDescendantResult bool

	// ListChildInstances
	childInstances []ipc.InstanceInfo

	// SendMessage
	sendResult string
	sendErr    error

	// StopInstance
	stopErr error

	// StartInstance
	startErr error

	// DeleteInstance
	deleteErr error

	// ListNodes
	nodes []ipc.NodeInfo
}

func (f *controllableFakeManager) IsDescendant(targetID, ancestorID string) bool {
	return f.isDescendantResult
}

func (f *controllableFakeManager) ListChildInstances(callerID string) []ipc.InstanceInfo {
	return f.childInstances
}

func (f *controllableFakeManager) SendMessage(_ context.Context, instanceID, message string, _ func(ipc.ChatEvent) error) (string, error) {
	return f.sendResult, f.sendErr
}

func (f *controllableFakeManager) StopInstance(id string) (ipc.InstanceInfo, error) {
	return ipc.InstanceInfo{}, f.stopErr
}

func (f *controllableFakeManager) StartInstance(_ context.Context, id string) error {
	return f.startErr
}

func (f *controllableFakeManager) DeleteInstance(id string) error {
	return f.deleteErr
}

func (f *controllableFakeManager) ListNodes() []ipc.NodeInfo {
	return f.nodes
}

func runMgmtTool(t *testing.T, tool fantasy.AgentTool, callerID, input string) fantasy.ToolResponse {
	t.Helper()
	ctx := ContextWithCallerID(context.Background(), callerID)
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

// --- ScopedManager tests ---

func TestScopedManager_CheckDescendant_Allowed(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: true}
	sm := NewScopedManager(mgr, "parent-1")
	if err := sm.checkDescendant("child-1"); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestScopedManager_CheckDescendant_Denied(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: false}
	sm := NewScopedManager(mgr, "parent-1")
	err := sm.checkDescendant("sibling-1")
	if err == nil {
		t.Fatal("expected error for non-descendant")
	}
	if !strings.Contains(err.Error(), "not a descendant") {
		t.Errorf("expected descendant error, got: %v", err)
	}
}

// --- ListNodes tests ---

func TestListNodes_Empty(t *testing.T) {
	mgr := &controllableFakeManager{nodes: nil}
	tool := buildListNodes(mgr, testLogger)

	resp := runMgmtTool(t, tool, "caller", `{}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "No cluster nodes") {
		t.Errorf("expected no nodes message, got: %s", resp.Content)
	}
}

func TestListNodes_WithNodes(t *testing.T) {
	mgr := &controllableFakeManager{
		nodes: []ipc.NodeInfo{
			{ID: "node-1", Name: "leader", Status: "online", IsHome: true, Capacity: 4, ActiveCount: 2},
			{ID: "node-2", Name: "worker-1", Status: "online", IsHome: false, Capacity: 8, ActiveCount: 0},
		},
	}
	tool := buildListNodes(mgr, testLogger)

	resp := runMgmtTool(t, tool, "caller", `{}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "leader (home)") {
		t.Errorf("expected home label, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "worker-1") {
		t.Errorf("expected worker name, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "capacity: 4") {
		t.Errorf("expected capacity, got: %s", resp.Content)
	}
}

func TestListNodes_ZeroCapacity(t *testing.T) {
	mgr := &controllableFakeManager{
		nodes: []ipc.NodeInfo{
			{ID: "n1", Name: "node", Status: "online", Capacity: 0, ActiveCount: 1},
		},
	}
	tool := buildListNodes(mgr, testLogger)

	resp := runMgmtTool(t, tool, "caller", `{}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	// Capacity 0 should not show "capacity: 0".
	if strings.Contains(resp.Content, "capacity:") {
		t.Errorf("zero capacity should be omitted, got: %s", resp.Content)
	}
}

// --- ResumeInstance tests ---

func TestResumeInstance_Success(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: true}
	tool := buildResumeInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "resumed") {
		t.Errorf("expected 'resumed' in response, got: %s", resp.Content)
	}
}

func TestResumeInstance_EmptyID(t *testing.T) {
	mgr := &controllableFakeManager{}
	tool := buildResumeInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":""}`)
	if !resp.IsError {
		t.Error("expected error for empty instance_id")
	}
}

func TestResumeInstance_NotDescendant(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: false}
	tool := buildResumeInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"sibling-1"}`)
	if !resp.IsError {
		t.Error("expected error for non-descendant")
	}
	if !strings.Contains(resp.Content, "not a descendant") {
		t.Errorf("expected descendant error, got: %s", resp.Content)
	}
}

func TestResumeInstance_StartError(t *testing.T) {
	mgr := &controllableFakeManager{
		isDescendantResult: true,
		startErr:           errors.New("instance not found"),
	}
	tool := buildResumeInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1"}`)
	if !resp.IsError {
		t.Error("expected error when start fails")
	}
	if !strings.Contains(resp.Content, "instance not found") {
		t.Errorf("expected error cause, got: %s", resp.Content)
	}
}

// --- ListInstances tests ---

func TestListInstances_Empty(t *testing.T) {
	mgr := &controllableFakeManager{childInstances: nil}
	tool := buildListInstances(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "No child instances") {
		t.Errorf("expected empty message, got: %s", resp.Content)
	}
}

func TestListInstances_WithInstances(t *testing.T) {
	mgr := &controllableFakeManager{
		childInstances: []ipc.InstanceInfo{
			{Name: "researcher", ID: "inst-1", Mode: "persistent", Status: "running", Description: "Research agent"},
			{Name: "coder", ID: "inst-2", Mode: "ephemeral", Status: "stopped"},
		},
	}
	tool := buildListInstances(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "researcher") {
		t.Error("expected researcher in listing")
	}
	if !strings.Contains(resp.Content, "inst-1") {
		t.Error("expected instance ID")
	}
	if !strings.Contains(resp.Content, "Research agent") {
		t.Error("expected description")
	}
	if !strings.Contains(resp.Content, "coder") {
		t.Error("expected coder in listing")
	}
}

// --- SendMessage tests ---

func TestSendMessage_Success(t *testing.T) {
	mgr := &controllableFakeManager{
		isDescendantResult: true,
		sendResult:         "Hello from child",
	}
	tool := buildSendMessage(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1","message":"How are you?"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if resp.Content != "Hello from child" {
		t.Errorf("expected child response, got: %s", resp.Content)
	}
}

func TestSendMessage_EmptyID(t *testing.T) {
	mgr := &controllableFakeManager{}
	tool := buildSendMessage(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"","message":"hi"}`)
	if !resp.IsError {
		t.Error("expected error for empty instance_id")
	}
}

func TestSendMessage_EmptyMessage(t *testing.T) {
	mgr := &controllableFakeManager{}
	tool := buildSendMessage(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1","message":""}`)
	if !resp.IsError {
		t.Error("expected error for empty message")
	}
}

func TestSendMessage_NotDescendant(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: false}
	tool := buildSendMessage(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"sibling","message":"hi"}`)
	if !resp.IsError {
		t.Error("expected error for non-descendant")
	}
}

func TestSendMessage_Error(t *testing.T) {
	mgr := &controllableFakeManager{
		isDescendantResult: true,
		sendErr:            errors.New("instance busy"),
	}
	tool := buildSendMessage(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1","message":"hi"}`)
	if !resp.IsError {
		t.Error("expected error on send failure")
	}
	if !strings.Contains(resp.Content, "instance busy") {
		t.Errorf("expected cause in error, got: %s", resp.Content)
	}
}

// --- StopInstance tests ---

func TestStopInstance_Success(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: true}
	tool := buildStopInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "stopped") {
		t.Errorf("expected 'stopped' in response, got: %s", resp.Content)
	}
}

func TestStopInstance_EmptyID(t *testing.T) {
	mgr := &controllableFakeManager{}
	tool := buildStopInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":""}`)
	if !resp.IsError {
		t.Error("expected error for empty instance_id")
	}
}

func TestStopInstance_NotDescendant(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: false}
	tool := buildStopInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"sibling"}`)
	if !resp.IsError {
		t.Error("expected error for non-descendant")
	}
}

func TestStopInstance_Error(t *testing.T) {
	mgr := &controllableFakeManager{
		isDescendantResult: true,
		stopErr:            errors.New("already stopped"),
	}
	tool := buildStopInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1"}`)
	if !resp.IsError {
		t.Error("expected error on stop failure")
	}
}

// --- DeleteInstance tests ---

func TestDeleteInstance_Success(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: true}
	tool := buildDeleteInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "deleted") {
		t.Errorf("expected 'deleted' in response, got: %s", resp.Content)
	}
}

func TestDeleteInstance_EmptyID(t *testing.T) {
	mgr := &controllableFakeManager{}
	tool := buildDeleteInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":""}`)
	if !resp.IsError {
		t.Error("expected error for empty instance_id")
	}
}

func TestDeleteInstance_NotDescendant(t *testing.T) {
	mgr := &controllableFakeManager{isDescendantResult: false}
	tool := buildDeleteInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"sibling"}`)
	if !resp.IsError {
		t.Error("expected error for non-descendant")
	}
}

func TestDeleteInstance_Error(t *testing.T) {
	mgr := &controllableFakeManager{
		isDescendantResult: true,
		deleteErr:          errors.New("not found"),
	}
	tool := buildDeleteInstance(mgr, testLogger)

	resp := runMgmtTool(t, tool, "parent", `{"instance_id":"child-1"}`)
	if !resp.IsError {
		t.Error("expected error on delete failure")
	}
}

// --- buildCoordinatorTools test ---

func TestBuildCoordinatorTools_Count(t *testing.T) {
	mgr := &controllableFakeManager{}
	tools := buildCoordinatorTools(mgr, testLogger)

	// Should return 6 tools: ResumeInstance, ListInstances, ListNodes, SendMessage, StopInstance, DeleteInstance
	if len(tools) != 6 {
		t.Errorf("expected 6 coordinator tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Info().Name] = true
	}

	expected := []string{"ResumeInstance", "ListInstances", "ListNodes", "SendMessage", "StopInstance", "DeleteInstance"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing coordinator tool: %s", name)
		}
	}
}

// --- CreatePersistentInstance error case ---

func TestCreatePersistentInstance_CreateError(t *testing.T) {
	mgr := &fakeHostManager{createErr: errors.New("agent not found")}
	tool := buildCreatePersistentInstanceTool(mgr, testLogger)

	resp := runTool(t, tool, `{"agent":"nonexistent"}`)
	if !resp.IsError {
		t.Error("expected error for failed creation")
	}
	if !strings.Contains(resp.Content, "agent not found") {
		t.Errorf("expected cause in error, got: %s", resp.Content)
	}
}

func TestCreatePersistentInstance_WithDisplayName(t *testing.T) {
	mgr := &fakeHostManager{createResult: "inst-789"}
	tool := buildCreatePersistentInstanceTool(mgr, testLogger)

	resp := runTool(t, tool, `{"agent":"researcher","name":"My Researcher"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "My Researcher") {
		t.Errorf("expected display name in response, got: %s", resp.Content)
	}
}
