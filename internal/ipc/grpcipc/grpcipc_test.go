package grpcipc_test

import (
	"context"
	"net"
	"testing"

	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// fakeWorker implements ipc.AgentWorker for testing.
type fakeWorker struct {
	shutdown bool
	toolResult ipc.ToolResult
	lastTool   struct{ callID, name, input string }
}

func (f *fakeWorker) ExecuteTool(_ context.Context, callID, name, input string) (ipc.ToolResult, error) {
	f.lastTool.callID = callID
	f.lastTool.name = name
	f.lastTool.input = input
	return f.toolResult, nil
}

func (f *fakeWorker) Shutdown(_ context.Context) error {
	f.shutdown = true
	return nil
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

func TestWorkerRoundtrip_ExecuteTool(t *testing.T) {
	worker := &fakeWorker{toolResult: ipc.ToolResult{Content: "file contents here"}}
	client := setupWorkerTest(t, worker)

	result, err := client.ExecuteTool(t.Context(), "call-1", "read_file", `{"path":"test.txt"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.Content != "file contents here" {
		t.Errorf("content = %q, want %q", result.Content, "file contents here")
	}
	if result.IsError {
		t.Error("unexpected is_error=true")
	}
	if worker.lastTool.name != "read_file" {
		t.Errorf("tool name = %q, want read_file", worker.lastTool.name)
	}
	if worker.lastTool.callID != "call-1" {
		t.Errorf("call_id = %q, want call-1", worker.lastTool.callID)
	}
}

func TestWorkerRoundtrip_ExecuteTool_Error(t *testing.T) {
	worker := &fakeWorker{toolResult: ipc.ToolResult{Content: "not found", IsError: true}}
	client := setupWorkerTest(t, worker)

	result, err := client.ExecuteTool(t.Context(), "call-2", "read_file", `{"path":"missing.txt"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !result.IsError {
		t.Error("expected is_error=true")
	}
	if result.Content != "not found" {
		t.Errorf("content = %q, want %q", result.Content, "not found")
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

func TestWorkerRoundtrip_SecretEnvInjection(t *testing.T) {
	worker := &fakeWorker{toolResult: ipc.ToolResult{Content: "ok"}}

	// Track secrets received by the server.
	var receivedSecrets []string
	ws := grpcipc.NewWorkerServer(worker)
	ws.SetSecretEnvCallback(func(env []string) {
		receivedSecrets = env
	})

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	ws.Register(srv)
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

	client := grpcipc.NewWorkerClient(conn)
	client.SetSecretEnvFn(func() []string {
		return []string{"API_KEY=sk-secret-123", "DB_PASS=hunter2"}
	})

	_, err = client.ExecuteTool(t.Context(), "call-1", "bash", `{"command":"echo $API_KEY"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}

	if len(receivedSecrets) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(receivedSecrets))
	}
	if receivedSecrets[0] != "API_KEY=sk-secret-123" {
		t.Errorf("secret[0] = %q, want API_KEY=sk-secret-123", receivedSecrets[0])
	}
	if receivedSecrets[1] != "DB_PASS=hunter2" {
		t.Errorf("secret[1] = %q, want DB_PASS=hunter2", receivedSecrets[1])
	}
}

func TestWorkerRoundtrip_NoSecrets(t *testing.T) {
	worker := &fakeWorker{toolResult: ipc.ToolResult{Content: "ok"}}

	// Verify callback is not invoked when no secrets are set.
	called := false
	ws := grpcipc.NewWorkerServer(worker)
	ws.SetSecretEnvCallback(func(env []string) {
		called = true
	})

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	ws.Register(srv)
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

	// No SetSecretEnvFn — should not send secrets.
	client := grpcipc.NewWorkerClient(conn)
	_, err = client.ExecuteTool(t.Context(), "call-1", "bash", `{"command":"ls"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if called {
		t.Error("secret callback should not be invoked when no secrets sent")
	}
}
