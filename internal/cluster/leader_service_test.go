package cluster_test

import (
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

// setupLeaderService creates a fully wired LeaderService with an mTLS-connected node.
// Returns the service, node ID, node stream, and cleanup function.
func setupLeaderService(t *testing.T) (*cluster.LeaderService, cluster.NodeID, pb.Cluster_NodeStreamClient) {
	t.Helper()

	registry := cluster.NewNodeRegistry()
	logger := slog.Default()
	clientID := testIdentityFromSeed(10)

	pending := cluster.NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := cluster.NewLeaderStream(registry, func(nodeID string) cluster.ApprovalStatus {
		if nodeID == nodeIDFromIdentity(clientID) {
			return cluster.ApprovalGranted
		}
		return cluster.ApprovalPending
	}, pending, logger)

	svc := cluster.NewLeaderService(ls, registry, logger)

	serverID := testIdentityFromSeed(0)
	serverCert, err := cluster.TLSCertFromIdentity(serverID)
	if err != nil {
		t.Fatal(err)
	}

	serverTLS := cluster.ServerTLSConfig(serverCert)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	ls.Register(srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
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
			Register: &pb.NodeRegister{NodeName: "test-node", Capacity: 4},
		},
	}); err != nil {
		t.Fatalf("send register: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	nodeID := resp.GetRegistered().NodeId

	return svc, nodeID, stream
}

func TestLeaderService_StreamAndRegistry(t *testing.T) {
	svc, _, _ := setupLeaderService(t)

	if svc.Stream() == nil {
		t.Fatal("Stream() returned nil")
	}
	if svc.Registry() == nil {
		t.Fatal("Registry() returned nil")
	}
}

func TestLeaderService_SetJobCompletionHandler(t *testing.T) {
	svc, nodeID, stream := setupLeaderService(t)

	var mu sync.Mutex
	var received *pb.JobCompletionNotify
	var receivedSessionID string
	done := make(chan struct{}, 1)
	svc.SetJobCompletionHandler(func(sessionID string, completion *pb.JobCompletionNotify) {
		mu.Lock()
		receivedSessionID = sessionID
		received = completion
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Send a job completion from the node.
	if err := stream.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_JobCompletion{
			JobCompletion: &pb.JobCompletionNotify{
				SessionId:   "sess-1",
				TaskId:      "task-1",
				Command:     "echo hello",
				Description: "test task",
				ExitCode:    0,
			},
		},
	}); err != nil {
		t.Fatalf("send job completion: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job completion handler was not called")
	}

	mu.Lock()
	defer mu.Unlock()
	if receivedSessionID != "sess-1" {
		t.Fatalf("session ID = %q, want %q", receivedSessionID, "sess-1")
	}
	if received.TaskId != "task-1" {
		t.Fatalf("task ID = %q, want %q", received.TaskId, "task-1")
	}
	_ = nodeID
}

func TestLeaderService_SetTerminalHandlers(t *testing.T) {
	svc, _, stream := setupLeaderService(t)

	done := make(chan struct{}, 1)
	var mu sync.Mutex
	var gotCreated, gotOutput, gotExited bool

	checkAllDone := func() {
		if gotCreated && gotOutput && gotExited {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}

	svc.SetTerminalHandlers(
		func(_ cluster.NodeID, msg *pb.TerminalCreated) {
			mu.Lock()
			gotCreated = true
			checkAllDone()
			mu.Unlock()
		},
		func(_ cluster.NodeID, msg *pb.TerminalOutput) {
			mu.Lock()
			gotOutput = true
			checkAllDone()
			mu.Unlock()
		},
		func(_ cluster.NodeID, msg *pb.TerminalExited) {
			mu.Lock()
			gotExited = true
			checkAllDone()
			mu.Unlock()
		},
	)

	// Send terminal messages from node.
	stream.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_TerminalCreated{
			TerminalCreated: &pb.TerminalCreated{SessionId: "term-1"},
		},
	})
	stream.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_TerminalOutput{
			TerminalOutput: &pb.TerminalOutput{SessionId: "term-1", Data: []byte("hello")},
		},
	})
	stream.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_TerminalExited{
			TerminalExited: &pb.TerminalExited{SessionId: "term-1", ExitCode: 0},
		},
	})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		if !gotCreated {
			t.Error("TerminalCreated handler not called")
		}
		if !gotOutput {
			t.Error("TerminalOutput handler not called")
		}
		if !gotExited {
			t.Error("TerminalExited handler not called")
		}
	}
}

func TestLeaderService_DeliverToolResult(t *testing.T) {
	svc, nodeID, stream := setupLeaderService(t)

	// Create a RemoteWorker and register it via SpawnOnNode handshake.
	rw := cluster.NewRemoteWorker(svc.Stream(), nodeID, "sess-deliver")

	// Manually wire up the worker in the service by using WireNodeHandlers.
	// The handlers are already wired by NewLeaderService, so we just need
	// to register the worker. We'll test deliverToolResult indirectly
	// through the full tool execution flow.

	// Node responds to tool calls.
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
							SessionId: et.SessionId,
							CallId:    et.CallId,
							Content:   "result-" + et.ToolName,
						},
					},
				})
			}
		}
	}()

	// Wire handlers to deliver results to our RemoteWorker.
	svc.Stream().SetHandlers(nodeID, &cluster.NodeHandlers{
		OnToolResult: func(_ cluster.NodeID, msg *pb.NodeToolResult) {
			var transportErr error
			rw.DeliverToolResult(msg.CallId, ipc.ToolResult{
				Content: msg.Content,
				IsError: msg.IsError,
			}, transportErr)
		},
	})

	result, err := rw.ExecuteTool(t.Context(), "call-1", "Bash", `{"command":"ls"}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.Content != "result-Bash" {
		t.Fatalf("content = %q, want %q", result.Content, "result-Bash")
	}

	rw.Close()
}

func TestLeaderService_SpawnOnNode_NodeNotFound(t *testing.T) {
	svc, _, _ := setupLeaderService(t)

	_, err := svc.SpawnOnNode(t.Context(), "nonexistent-node", cluster.SpawnRequest{
		SessionID: "sess-1",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

func TestLeaderService_SpawnOnNode_NodeOffline(t *testing.T) {
	svc, nodeID, _ := setupLeaderService(t)

	// Set node offline.
	svc.Registry().SetOffline(nodeID)

	_, err := svc.SpawnOnNode(t.Context(), nodeID, cluster.SpawnRequest{
		SessionID: "sess-1",
	})
	if err == nil {
		t.Fatal("expected error for offline node")
	}
}
