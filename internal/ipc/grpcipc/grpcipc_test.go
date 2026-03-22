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

// fakeHost implements ipc.AgentHost for testing.
type fakeHost struct {
	spawnResult  string
	startID      string
	sendResult   string
	agents       []ipc.AgentInfo
	secretNames  []string
	secretEnv    []string
	lastSpawnReq struct{ name, prompt string }
	lastSendReq  struct{ id, message string }
}

func (f *fakeHost) SpawnAgent(ctx context.Context, agentName, prompt string, onDelta func(string) error) (string, error) {
	f.lastSpawnReq.name = agentName
	f.lastSpawnReq.prompt = prompt
	if onDelta != nil {
		onDelta("thinking...")
		onDelta("done thinking")
	}
	return f.spawnResult, nil
}

func (f *fakeHost) StartAgent(ctx context.Context, agentName string) (string, error) {
	return f.startID, nil
}

func (f *fakeHost) SendMessage(ctx context.Context, agentID, message string, onDelta func(string) error) (string, error) {
	f.lastSendReq.id = agentID
	f.lastSendReq.message = message
	if onDelta != nil {
		onDelta("streaming...")
	}
	return f.sendResult, nil
}

func (f *fakeHost) StopAgent(ctx context.Context, agentID string) error {
	if agentID == "not-found" {
		return fmt.Errorf("agent %q not found", agentID)
	}
	return nil
}

func (f *fakeHost) ListAgents(ctx context.Context) ([]ipc.AgentInfo, error) {
	return f.agents, nil
}

func (f *fakeHost) GetSecrets(ctx context.Context) ([]string, []string, error) {
	return f.secretNames, f.secretEnv, nil
}

// fakeWorker implements ipc.AgentWorker for testing.
type fakeWorker struct {
	chatResult string
	shutdown   bool
}

func (f *fakeWorker) Chat(ctx context.Context, message string, onDelta func(string) error) (string, error) {
	if onDelta != nil {
		onDelta("token1")
		onDelta("token2")
	}
	return f.chatResult, nil
}

func (f *fakeWorker) Shutdown(ctx context.Context) error {
	f.shutdown = true
	return nil
}

// setupHostTest creates a bufconn-based gRPC server/client pair for AgentHost.
func setupHostTest(t *testing.T, host ipc.AgentHost) *grpcipc.HostClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	grpcipc.NewHostServer(host).Register(srv)
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

	return grpcipc.NewHostClient(conn)
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
	host := &fakeHost{spawnResult: "task completed successfully"}
	client := setupHostTest(t, host)

	var deltas []string
	result, err := client.SpawnAgent(t.Context(), "researcher", "find info", func(text string) error {
		deltas = append(deltas, text)
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
	if host.lastSpawnReq.name != "researcher" {
		t.Errorf("agent name = %q, want researcher", host.lastSpawnReq.name)
	}
}

func TestHostRoundtrip_StartAgent(t *testing.T) {
	host := &fakeHost{startID: "session-123"}
	client := setupHostTest(t, host)

	id, err := client.StartAgent(t.Context(), "coordinator")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	if id != "session-123" {
		t.Errorf("id = %q, want session-123", id)
	}
}

func TestHostRoundtrip_SendMessage(t *testing.T) {
	host := &fakeHost{sendResult: "hello back"}
	client := setupHostTest(t, host)

	var deltas []string
	result, err := client.SendMessage(t.Context(), "agent-1", "hello", func(text string) error {
		deltas = append(deltas, text)
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
	host := &fakeHost{}
	client := setupHostTest(t, host)

	if err := client.StopAgent(t.Context(), "agent-1"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
}

func TestHostRoundtrip_StopAgent_NotFound(t *testing.T) {
	host := &fakeHost{}
	client := setupHostTest(t, host)

	err := client.StopAgent(t.Context(), "not-found")
	if err == nil {
		t.Fatal("expected error for not-found agent")
	}
}

func TestHostRoundtrip_ListAgents(t *testing.T) {
	host := &fakeHost{
		agents: []ipc.AgentInfo{
			{ID: "a1", Name: "researcher", Mode: "ephemeral", Description: "finds stuff"},
			{ID: "a2", Name: "writer", Mode: "persistent"},
		},
	}
	client := setupHostTest(t, host)

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
	host := &fakeHost{
		secretNames: []string{"TOKEN", "KEY"},
		secretEnv:   []string{"TOKEN=abc", "KEY=xyz"},
	}
	client := setupHostTest(t, host)

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

func TestWorkerRoundtrip_Chat(t *testing.T) {
	worker := &fakeWorker{chatResult: "I'm an agent"}
	client := setupWorkerTest(t, worker)

	var deltas []string
	result, err := client.Chat(t.Context(), "hello agent", func(text string) error {
		deltas = append(deltas, text)
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
