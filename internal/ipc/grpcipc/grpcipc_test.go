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
	createID    string
	sendResult  string
	children    []ipc.SessionInfo
	secretNames []string
	secretEnv   []string
	descendants map[string]string // targetID -> ancestorID (for IsDescendant)

	lastSpawnReq  struct{ name, prompt, parentID string }
	lastSendReq   struct{ id, message string }
	lastCreateReq struct{ name, parentID string }
	lastStopReq   string
}

func (f *fakeManager) SpawnSession(ctx context.Context, agentName, prompt, parentID string, onEvent func(ipc.ChatEvent) error) (string, error) {
	f.lastSpawnReq.name = agentName
	f.lastSpawnReq.prompt = prompt
	f.lastSpawnReq.parentID = parentID
	if onEvent != nil {
		onEvent(ipc.ChatEvent{Type: "delta", Content: "thinking..."})
		onEvent(ipc.ChatEvent{Type: "delta", Content: "done thinking"})
	}
	return f.spawnResult, nil
}

func (f *fakeManager) CreateSession(ctx context.Context, name, parentID string) (string, error) {
	f.lastCreateReq.name = name
	f.lastCreateReq.parentID = parentID
	return f.createID, nil
}

func (f *fakeManager) SendMessage(ctx context.Context, sessionID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	f.lastSendReq.id = sessionID
	f.lastSendReq.message = message
	if onEvent != nil {
		onEvent(ipc.ChatEvent{Type: "delta", Content: "streaming..."})
	}
	return f.sendResult, nil
}

func (f *fakeManager) StopSession(sessionID string) (ipc.SessionInfo, error) {
	f.lastStopReq = sessionID
	if sessionID == "not-found" {
		return ipc.SessionInfo{}, fmt.Errorf("session %q not found", sessionID)
	}
	return ipc.SessionInfo{ID: sessionID}, nil
}

func (f *fakeManager) StartSession(ctx context.Context, sessionID string) error {
	if sessionID == "not-found" {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return nil
}

func (f *fakeManager) DeleteSession(sessionID string) error {
	if sessionID == "not-found" {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return nil
}

func (f *fakeManager) IsDescendant(targetID, ancestorID string) bool {
	if f.descendants == nil {
		return true // default: allow all
	}
	ancestor, ok := f.descendants[targetID]
	return ok && ancestor == ancestorID
}

func (f *fakeManager) ListChildSessions(callerID string) []ipc.SessionInfo {
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

func TestHostRoundtrip_SpawnSession(t *testing.T) {
	mgr := &fakeManager{spawnResult: "task completed successfully"}
	client := setupHostTest(t, mgr, "parent-1")

	var deltas []string
	result, err := client.SpawnSession(t.Context(), "researcher", "find info", func(evt ipc.ChatEvent) error {
		if evt.Type == "delta" {
			deltas = append(deltas, evt.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SpawnSession: %v", err)
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

func TestHostRoundtrip_CreateSession(t *testing.T) {
	mgr := &fakeManager{createID: "session-123"}
	client := setupHostTest(t, mgr, "parent-1")

	id, err := client.CreateSession(t.Context(), "coordinator")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if id != "session-123" {
		t.Errorf("id = %q, want session-123", id)
	}
	if mgr.lastCreateReq.parentID != "parent-1" {
		t.Errorf("parent_id = %q, want parent-1", mgr.lastCreateReq.parentID)
	}
}

func TestHostRoundtrip_SendMessage(t *testing.T) {
	mgr := &fakeManager{sendResult: "hello back"}
	client := setupHostTest(t, mgr, "parent-1")

	var deltas []string
	result, err := client.SendMessage(t.Context(), "session-1", "hello", func(evt ipc.ChatEvent) error {
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

func TestHostRoundtrip_StopSession(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	if err := client.StopSession(t.Context(), "session-1"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if mgr.lastStopReq != "session-1" {
		t.Errorf("stopped = %q, want session-1", mgr.lastStopReq)
	}
}

func TestHostRoundtrip_StopSession_NotFound(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	err := client.StopSession(t.Context(), "not-found")
	if err == nil {
		t.Fatal("expected error for not-found session")
	}
}

func TestHostRoundtrip_StartSession(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	if err := client.StartSession(t.Context(), "session-1"); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
}

func TestHostRoundtrip_StartSession_NotFound(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	err := client.StartSession(t.Context(), "not-found")
	if err == nil {
		t.Fatal("expected error for not-found session")
	}
}

func TestHostRoundtrip_DeleteSession(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	if err := client.DeleteSession(t.Context(), "session-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
}

func TestHostRoundtrip_DeleteSession_NotFound(t *testing.T) {
	mgr := &fakeManager{}
	client := setupHostTest(t, mgr, "parent-1")

	err := client.DeleteSession(t.Context(), "not-found")
	if err == nil {
		t.Fatal("expected error for not-found session")
	}
}

func TestHostRoundtrip_ListSessions(t *testing.T) {
	mgr := &fakeManager{
		children: []ipc.SessionInfo{
			{ID: "s1", Name: "researcher", Mode: "ephemeral", Description: "finds stuff", Status: "running"},
			{ID: "s2", Name: "writer", Mode: "persistent", Status: "stopped"},
		},
	}
	client := setupHostTest(t, mgr, "parent-1")

	sessions, err := client.ListSessions(t.Context())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].Name != "researcher" {
		t.Errorf("sessions[0].Name = %q, want researcher", sessions[0].Name)
	}
	if sessions[0].Status != "running" {
		t.Errorf("sessions[0].Status = %q, want running", sessions[0].Status)
	}
	if sessions[1].Status != "stopped" {
		t.Errorf("sessions[1].Status = %q, want stopped", sessions[1].Status)
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

func TestHostRoundtrip_StopSession_DescendantCheck(t *testing.T) {
	mgr := &fakeManager{
		descendants: map[string]string{
			"child-1": "parent-1",
		},
	}

	// parent-2 cannot stop child-1
	client := setupHostTest(t, mgr, "parent-2")
	err := client.StopSession(t.Context(), "child-1")
	if err == nil {
		t.Fatal("expected StopSession to fail for non-descendant")
	}
}

func TestHostRoundtrip_DeleteSession_DescendantCheck(t *testing.T) {
	mgr := &fakeManager{
		descendants: map[string]string{
			"child-1": "parent-1",
		},
	}

	// parent-2 cannot delete child-1
	client := setupHostTest(t, mgr, "parent-2")
	err := client.DeleteSession(t.Context(), "child-1")
	if err == nil {
		t.Fatal("expected DeleteSession to fail for non-descendant")
	}
}

func TestHostRoundtrip_StartSession_DescendantCheck(t *testing.T) {
	mgr := &fakeManager{
		descendants: map[string]string{
			"child-1": "parent-1",
		},
	}

	// parent-2 cannot start child-1
	client := setupHostTest(t, mgr, "parent-2")
	err := client.StartSession(t.Context(), "child-1")
	if err == nil {
		t.Fatal("expected StartSession to fail for non-descendant")
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
