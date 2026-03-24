package grpcipc_test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// fakeManager implements ipc.HostManager for testing.
type fakeManager struct {
	spawnResult string
	startID     string
	sendResult  string
	children    []ipc.AgentInfo
	secretNames []string
	secretEnv   []string
	descendants map[string]string // targetID -> ancestorID (for IsDescendant)

	lastSpawnReq struct{ name, prompt, parentID string }
	lastSendReq  struct{ id, message string }
	lastStartReq struct{ name, parentID string }
	lastStopReq  string
}

func (f *fakeManager) SpawnSubagent(ctx context.Context, agentName, prompt, parentID string, onEvent func(ipc.ChatEvent) error) (string, error) {
	f.lastSpawnReq.name = agentName
	f.lastSpawnReq.prompt = prompt
	f.lastSpawnReq.parentID = parentID
	if onEvent != nil {
		onEvent(ipc.ChatEvent{Type: "delta", Content: "thinking..."})
		onEvent(ipc.ChatEvent{Type: "delta", Content: "done thinking"})
	}
	return f.spawnResult, nil
}

func (f *fakeManager) StartAgent(ctx context.Context, name, parentID string) (string, error) {
	f.lastStartReq.name = name
	f.lastStartReq.parentID = parentID
	return f.startID, nil
}

func (f *fakeManager) SendMessage(ctx context.Context, agentID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	f.lastSendReq.id = agentID
	f.lastSendReq.message = message
	if onEvent != nil {
		onEvent(ipc.ChatEvent{Type: "delta", Content: "streaming..."})
	}
	return f.sendResult, nil
}

func (f *fakeManager) StopAgent(agentID string) (ipc.AgentInfo, error) {
	f.lastStopReq = agentID
	if agentID == "not-found" {
		return ipc.AgentInfo{}, fmt.Errorf("agent %q not found", agentID)
	}
	return ipc.AgentInfo{ID: agentID}, nil
}

func (f *fakeManager) IsDescendant(targetID, ancestorID string) bool {
	if f.descendants == nil {
		return true // default: allow all
	}
	ancestor, ok := f.descendants[targetID]
	return ok && ancestor == ancestorID
}

func (f *fakeManager) ListChildren(callerID string) []ipc.AgentInfo {
	return f.children
}

func (f *fakeManager) SecretNames() []string {
	return f.secretNames
}

func (f *fakeManager) SecretEnv() []string {
	return f.secretEnv
}

// fakeWorker implements ipc.AgentWorker for testing.
type fakeWorker struct {
	chatResult string
	shutdown   bool
}

func (f *fakeWorker) Chat(ctx context.Context, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	if onEvent != nil {
		onEvent(ipc.ChatEvent{Type: "delta", Content: "token1"})
		onEvent(ipc.ChatEvent{Type: "delta", Content: "token2"})
	}
	return f.chatResult, nil
}

func (f *fakeWorker) Shutdown(ctx context.Context) error {
	f.shutdown = true
	return nil
}

// setupHostTest creates a bufconn-based gRPC server/client pair for AgentHost.
func setupHostTest(t *testing.T, mgr ipc.HostManager, callerID string) *grpcipc.HostClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	grpcipc.NewHostServer(mgr).Register(srv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return grpcipc.NewHostClient(conn, callerID)
}

// setupWorkerTest creates a bufconn-based gRPC server/client pair for AgentWorker.
func setupWorkerTest(t *testing.T, worker ipc.AgentWorker) *grpcipc.WorkerClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	grpcipc.NewWorkerServer(worker).Register(srv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return grpcipc.NewWorkerClient(conn)
}

func TestHostRoundtrip_SpawnAgent(t *testing.T) {
	mgr := &fakeManager{spawnResult: "task completed successfully"}
	client := setupHostTest(t, mgr, "parent-1")

	var deltas []string
	result, err := client.SpawnAgent(t.Context(), "researcher", "find info", func(evt ipc.ChatEvent) error {
		if evt.Type == "delta" {
			deltas = append(deltas, evt.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}
	if result != "task completed successfully" {
		t.Errorf("result = %q, want %q", result, "task completed successfully")
	}
	if len(deltas) != 2 {
		t.Errorf("got %d deltas, want 2", len(deltas))
	}
	if mgr.lastSpawnReq.name != "researcher" {
		t.Errorf("agent name = %q, want researcher", mgr.lastSpawnReq.name)
	}
	if mgr.lastSpawnReq.parentID != "parent-1" {
		t.Errorf("parent_id = %q, want parent-1", mgr.lastSpawnReq.parentID)
	}
}

func TestHostRoundtrip_StartAgent(t *testing.T) {
	mgr := &fakeManager{startID: "session-123"}
	client := setupHostTest(t, mgr, "parent-1")

	id, err := client.StartAgent(t.Context(), "coordinator")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	if id != "session-123" {
		t.Errorf("id = %q, want session-123", id)
	}
	if mgr.lastStartReq.parentID != "parent-1" {
		t.Errorf("parent_id = %q, want parent-1", mgr.lastStartReq.parentID)
	}
}

func TestHostRoundtrip_SendMessage(t *testing.T) {
	mgr := &fakeManager{sendResult: "hello back"}
	client := setupHostTest(t, mgr, "parent-1")

	var deltas []string
	result, err := client.SendMessage(t.Context(), "agent-1", "hello", func(evt ipc.ChatEvent) error {
		if evt.Type == "delta" {
			deltas = append(deltas, evt.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result != "hello back" {
		t.Errorf("result = %q, want %q", result, "hello back")
	}
	if len(deltas) != 1 {
		t.Errorf("got %d deltas, want 1", len(deltas))
	}
}

func TestHostRoundtrip_StopAgent(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	if err := client.StopAgent(t.Context(), "agent-1"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	if mgr.lastStopReq != "agent-1" {
		t.Errorf("stopped = %q, want agent-1", mgr.lastStopReq)
	}
}

func TestHostRoundtrip_StopAgent_NotFound(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	err := client.StopAgent(t.Context(), "not-found")
	if err == nil {
		t.Fatal("expected error for not-found agent")
	}
}

func TestHostRoundtrip_ListAgents(t *testing.T) {
	mgr := &fakeManager{
		children: []ipc.AgentInfo{
			{ID: "a1", Name: "researcher", Mode: "ephemeral", Description: "finds stuff"},
			{ID: "a2", Name: "writer", Mode: "persistent"},
		},
	}
	client := setupHostTest(t, mgr, "parent-1")

	agents, err := client.ListAgents(t.Context())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	if agents[0].Name != "researcher" {
		t.Errorf("agent[0].Name = %q, want researcher", agents[0].Name)
	}
	if agents[1].Description != "" {
		t.Errorf("agent[1].Description = %q, want empty", agents[1].Description)
	}
}

func TestHostRoundtrip_GetSecrets(t *testing.T) {
	mgr := &fakeManager{
		secretNames: []string{"TOKEN", "KEY"},
		secretEnv:   []string{"TOKEN=abc", "KEY=xyz"},
	}
	client := setupHostTest(t, mgr, "parent-1")

	names, env, err := client.GetSecrets(t.Context())
	if err != nil {
		t.Fatalf("GetSecrets: %v", err)
	}
	if len(names) != 2 || names[0] != "TOKEN" {
		t.Errorf("names = %v, want [TOKEN KEY]", names)
	}
	if len(env) != 2 || env[0] != "TOKEN=abc" {
		t.Errorf("env = %v, want [TOKEN=abc KEY=xyz]", env)
	}
}

func TestHostRoundtrip_SendMessage_DescendantCheck(t *testing.T) {
	mgr := &fakeManager{
		sendResult: "ok",
		descendants: map[string]string{
			"child-1": "parent-1", // child-1 is a descendant of parent-1
		},
	}

	// parent-1 can message child-1
	client := setupHostTest(t, mgr, "parent-1")
	_, err := client.SendMessage(t.Context(), "child-1", "hello", nil)
	if err != nil {
		t.Fatalf("expected SendMessage to succeed for descendant: %v", err)
	}

	// parent-2 cannot message child-1
	client2 := setupHostTest(t, mgr, "parent-2")
	_, err = client2.SendMessage(t.Context(), "child-1", "hello", nil)
	if err == nil {
		t.Fatal("expected SendMessage to fail for non-descendant")
	}
}

func TestHostRoundtrip_StopAgent_DescendantCheck(t *testing.T) {
	mgr := &fakeManager{
		descendants: map[string]string{
			"child-1": "parent-1",
		},
	}

	// parent-2 cannot stop child-1
	client := setupHostTest(t, mgr, "parent-2")
	err := client.StopAgent(t.Context(), "child-1")
	if err == nil {
		t.Fatal("expected StopAgent to fail for non-descendant")
	}
}

func TestWorkerRoundtrip_Chat(t *testing.T) {
	worker := &fakeWorker{chatResult: "I'm an agent"}
	client := setupWorkerTest(t, worker)

	var deltas []string
	result, err := client.Chat(t.Context(), "hello agent", func(evt ipc.ChatEvent) error {
		if evt.Type == "delta" {
			deltas = append(deltas, evt.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result != "I'm an agent" {
		t.Errorf("result = %q, want %q", result, "I'm an agent")
	}
	if len(deltas) != 2 {
		t.Errorf("got %d deltas, want 2", len(deltas))
	}
	if deltas[0] != "token1" || deltas[1] != "token2" {
		t.Errorf("deltas = %v, want [token1 token2]", deltas)
	}
}

func TestWorkerRoundtrip_Shutdown(t *testing.T) {
	worker := &fakeWorker{}
	client := setupWorkerTest(t, worker)

	if err := client.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !worker.shutdown {
		t.Error("expected shutdown to be called")
	}
}
