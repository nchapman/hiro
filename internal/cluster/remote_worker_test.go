package cluster_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/ipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// setupRemoteWorkerTest sets up a leader + connected node over mTLS and returns
// a RemoteWorker that can execute tools on the node.
func setupRemoteWorkerTest(t *testing.T) (*cluster.RemoteWorker, pb.Cluster_NodeStreamClient, *cluster.LeaderStream) {
	t.Helper()

	registry := cluster.NewNodeRegistry()
	logger := slog.Default()
	clientID := testIdentityFromSeed(10)

	pending := cluster.NewPendingRegistry(filepath.Join(t.TempDir(), "pending.json"), nil)
	leader := cluster.NewLeaderStream(registry, func(nodeID string) cluster.ApprovalStatus {
		if nodeID == nodeIDFromIdentity(clientID) {
			return cluster.ApprovalGranted
		}
		return cluster.ApprovalPending
	}, pending, logger)

	serverID := testIdentityFromSeed(0)
	serverCert, err := cluster.TLSCertFromIdentity(serverID)
	if err != nil {
		t.Fatal(err)
	}

	serverTLS := cluster.ServerTLSConfig(serverCert)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	leader.Register(srv)

	lis, lisErr := net.Listen("tcp", "127.0.0.1:0")
	if lisErr != nil {
		t.Fatal(lisErr)
	}
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	// Connect with mTLS.
	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS := cluster.ClientTLSConfig(clientCert, serverID.PublicKey)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	client := pb.NewClusterClient(conn)
	stream, err := client.NodeStream(t.Context())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Register node.
	if err := stream.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_Register{
			Register: &pb.NodeRegister{
				NodeName: "rw-node",
			},
		},
	}); err != nil {
		t.Fatalf("send register: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	nodeID := resp.GetRegistered().NodeId

	// Create RemoteWorker.
	rw := cluster.NewRemoteWorker(leader, nodeID, "session-test")

	// Wire up handlers so tool results reach the RemoteWorker.
	leader.SetHandlers(nodeID, &cluster.NodeHandlers{
		OnToolResult: func(_ cluster.NodeID, msg *pb.NodeToolResult) {
			var err error
			if msg.Error != "" {
				err = nil // transport error would be set here
			}
			rw.DeliverToolResult(msg.CallId, ipc.ToolResult{
				Content: msg.Content,
				IsError: msg.IsError,
			}, err)
		},
	})

	return rw, stream, leader
}

func TestRemoteWorker_ExecuteTool(t *testing.T) {
	rw, stream, _ := setupRemoteWorkerTest(t)
	defer rw.Close()

	// Node reads and responds to tool requests in background.
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if et := msg.GetExecuteTool(); et != nil {
				stream.Send(&pb.NodeMessage{
					Msg: &pb.NodeMessage_ToolResult{
						ToolResult: &pb.NodeToolResult{
							CallId:  et.CallId,
							Content: "result from " + et.ToolName,
							IsError: false,
						},
					},
				})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	result, err := rw.ExecuteTool(ctx, "call-1", "Read", `{"file_path":"test.txt"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.Content != "result from Read" {
		t.Errorf("content = %q, want %q", result.Content, "result from Read")
	}
	if result.IsError {
		t.Error("unexpected is_error=true")
	}
}

func TestRemoteWorker_ExecuteTool_Error(t *testing.T) {
	rw, stream, _ := setupRemoteWorkerTest(t)
	defer rw.Close()

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if et := msg.GetExecuteTool(); et != nil {
				stream.Send(&pb.NodeMessage{
					Msg: &pb.NodeMessage_ToolResult{
						ToolResult: &pb.NodeToolResult{
							CallId:  et.CallId,
							Content: "file not found",
							IsError: true,
						},
					},
				})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	result, err := rw.ExecuteTool(ctx, "call-2", "Read", `{"file_path":"missing.txt"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !result.IsError {
		t.Error("expected is_error=true")
	}
	if result.Content != "file not found" {
		t.Errorf("content = %q, want %q", result.Content, "file not found")
	}
}

func TestRemoteWorker_ExecuteTool_WithSecrets(t *testing.T) {
	rw, stream, _ := setupRemoteWorkerTest(t)
	defer rw.Close()

	rw.SetSecretEnvFn(func() []string {
		return []string{"API_KEY=secret123"}
	})

	var receivedSecrets []string
	var secretsMu sync.Mutex

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if et := msg.GetExecuteTool(); et != nil {
				secretsMu.Lock()
				receivedSecrets = et.SecretEnv
				secretsMu.Unlock()
				stream.Send(&pb.NodeMessage{
					Msg: &pb.NodeMessage_ToolResult{
						ToolResult: &pb.NodeToolResult{
							CallId:  et.CallId,
							Content: "ok",
						},
					},
				})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := rw.ExecuteTool(ctx, "call-3", "Bash", `{"command":"echo $API_KEY"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}

	secretsMu.Lock()
	if len(receivedSecrets) != 1 || receivedSecrets[0] != "API_KEY=secret123" {
		t.Errorf("secrets = %v, want [API_KEY=secret123]", receivedSecrets)
	}
	secretsMu.Unlock()

	// Non-Bash tools should not receive secrets.
	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel2()

	_, err = rw.ExecuteTool(ctx2, "call-4", "Read", `{"file_path":"/tmp/foo"}`)
	if err != nil {
		t.Fatalf("ExecuteTool(Read): %v", err)
	}

	secretsMu.Lock()
	defer secretsMu.Unlock()
	if len(receivedSecrets) != 0 {
		t.Errorf("expected 0 secrets for Read tool, got %v", receivedSecrets)
	}
}

func TestRemoteWorker_Close(t *testing.T) {
	rw, _, _ := setupRemoteWorkerTest(t)

	rw.Close()

	// ExecuteTool after close should fail.
	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()

	_, err := rw.ExecuteTool(ctx, "call-x", "bash", `{}`)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestRemoteWorker_ContextCancellation(t *testing.T) {
	rw, _, _ := setupRemoteWorkerTest(t)
	defer rw.Close()

	// Don't set up a responder — the call should time out.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err := rw.ExecuteTool(ctx, "call-timeout", "bash", `{}`)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRemoteWorker_ConcurrentCalls(t *testing.T) {
	rw, stream, _ := setupRemoteWorkerTest(t)
	defer rw.Close()

	// Node echoes back call ID as content.
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if et := msg.GetExecuteTool(); et != nil {
				stream.Send(&pb.NodeMessage{
					Msg: &pb.NodeMessage_ToolResult{
						ToolResult: &pb.NodeToolResult{
							CallId:  et.CallId,
							Content: "result-" + et.CallId,
						},
					},
				})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			callID := fmt.Sprintf("concurrent-%d", id)
			result, err := rw.ExecuteTool(ctx, callID, "bash", `{}`)
			if err != nil {
				t.Errorf("call %s: %v", callID, err)
				return
			}
			expected := "result-" + callID
			if result.Content != expected {
				t.Errorf("call %s: content = %q, want %q", callID, result.Content, expected)
			}
		}(i)
	}
	wg.Wait()
}
