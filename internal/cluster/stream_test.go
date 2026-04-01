package cluster_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// testIdentityFromSeed creates a deterministic identity for testing.
func testIdentityFromSeed(seed byte) *cluster.NodeIdentity {
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	priv := ed25519.NewKeyFromSeed(s)
	pub := priv.Public().(ed25519.PublicKey)
	hash := sha256.Sum256(pub)
	return &cluster.NodeIdentity{PrivateKey: priv, PublicKey: pub, NodeID: hex.EncodeToString(hash[:])}
}

// nodeIDFromIdentity derives the node ID from an identity (same as LeaderStream does).
func nodeIDFromIdentity(id *cluster.NodeIdentity) string {
	return id.NodeID
}

// setupApprovalTest creates an mTLS-based leader gRPC server with approval-based auth.
// approvedIDs is the set of node IDs that are pre-approved.
func setupApprovalTest(t *testing.T, registry *cluster.NodeRegistry, approvedIDs map[string]bool) (*cluster.LeaderStream, string, *cluster.NodeIdentity, *cluster.PendingRegistry) {
	t.Helper()
	logger := slog.Default()

	pending := cluster.NewPendingRegistry(filepath.Join(t.TempDir(), "pending.json"))

	leader := cluster.NewLeaderStream(registry, func(nodeID string) bool {
		return approvedIDs[nodeID]
	}, pending, logger)

	serverID := testIdentityFromSeed(0)
	serverCert, err := cluster.TLSCertFromIdentity(serverID)
	if err != nil {
		t.Fatal(err)
	}

	serverTLS := cluster.ServerTLSConfig(serverCert)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	leader.Register(srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	return leader, lis.Addr().String(), serverID, pending
}

func TestStream_ApprovedRegistration(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	clientID := testIdentityFromSeed(1)

	// Pre-approve the client identity.
	approved := map[string]bool{nodeIDFromIdentity(clientID): true}
	_, addr, serverID, pending := setupApprovalTest(t, registry, approved)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS := cluster.ClientTLSConfig(clientCert, serverID.PublicKey)

	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: addr,
		NodeName:   "approved-node",
		Capacity:   4,
		TLSConfig:  clientTLS,
		Logger:     slog.Default(),
	})

	var connectErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		connectErr = ws.Connect(ctx)
	}()

	// Wait for registration to appear in registry.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		nodes := registry.List()
		for _, n := range nodes {
			if n.Name == "approved-node" {
				if n.Capacity != 4 {
					t.Errorf("expected capacity 4, got %d", n.Capacity)
				}
				// Node ID should be the identity-derived ID, not random.
				if string(n.ID) != nodeIDFromIdentity(clientID) {
					t.Errorf("node ID = %q, want %q", n.ID, nodeIDFromIdentity(clientID))
				}
				// Should not be in pending.
				if pending.Count() != 0 {
					t.Error("approved node should not be in pending list")
				}
				cancel()
				wg.Wait()
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()
	if connectErr != nil {
		t.Fatalf("connect error: %v", connectErr)
	}
	t.Fatal("approved-node never appeared in registry")
}

func TestStream_PendingApproval(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	clientID := testIdentityFromSeed(1)

	// Do NOT approve the client.
	approved := map[string]bool{}
	_, addr, serverID, pending := setupApprovalTest(t, registry, approved)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS := cluster.ClientTLSConfig(clientCert, serverID.PublicKey)

	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: addr,
		NodeName:   "unapproved-node",
		Capacity:   2,
		TLSConfig:  clientTLS,
		Logger:     slog.Default(),
	})

	err = ws.Connect(ctx)
	if err != cluster.ErrPendingApproval {
		t.Fatalf("expected ErrPendingApproval, got %v", err)
	}

	// Node should NOT be in the registry.
	if registry.Len() != 0 {
		t.Error("unapproved node should not be registered")
	}

	// Node SHOULD be in the pending list.
	nodes := pending.List()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 pending node, got %d", len(nodes))
	}
	if nodes[0].NodeID != nodeIDFromIdentity(clientID) {
		t.Errorf("pending node ID = %q, want %q", nodes[0].NodeID, nodeIDFromIdentity(clientID))
	}
	if nodes[0].Name != "unapproved-node" {
		t.Errorf("pending node name = %q, want %q", nodes[0].Name, "unapproved-node")
	}
}

func TestStream_ToolExecution(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	clientID := testIdentityFromSeed(1)
	approved := map[string]bool{nodeIDFromIdentity(clientID): true}
	leader, addr, serverID, _ := setupApprovalTest(t, registry, approved)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS := cluster.ClientTLSConfig(clientCert, serverID.PublicKey)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewClusterClient(conn)
	stream, err := client.NodeStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Register.
	if err := stream.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_Register{
			Register: &pb.NodeRegister{
				NodeName: "tool-node",
			},
		},
	}); err != nil {
		t.Fatalf("send register: %v", err)
	}

	// Receive registration confirmation.
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	nodeID := cluster.NodeID(resp.GetRegistered().NodeId)

	// Node reads messages in background and handles tool requests.
	var toolReceived *pb.ExecuteToolRemote
	var nodeWg sync.WaitGroup
	nodeWg.Add(1)
	go func() {
		defer nodeWg.Done()
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if et := msg.GetExecuteTool(); et != nil {
				toolReceived = et
				stream.Send(&pb.NodeMessage{
					Msg: &pb.NodeMessage_ToolResult{
						ToolResult: &pb.NodeToolResult{
							CallId:  et.CallId,
							Content: "hello from node",
							IsError: false,
						},
					},
				})
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)

	var resultReceived *pb.NodeToolResult
	var resultMu sync.Mutex
	resultCh := make(chan struct{}, 1)

	leader.SetHandlers(nodeID, &cluster.NodeHandlers{
		OnToolResult: func(_ cluster.NodeID, msg *pb.NodeToolResult) {
			resultMu.Lock()
			resultReceived = msg
			resultMu.Unlock()
			resultCh <- struct{}{}
		},
	})

	err = leader.SendToNode(nodeID, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_ExecuteTool{
			ExecuteTool: &pb.ExecuteToolRemote{
				SessionId: "session-1",
				CallId:    "call-42",
				ToolName:  "bash",
				Input:     `{"command":"echo hello"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("send execute tool: %v", err)
	}

	select {
	case <-resultCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for tool result")
	}

	resultMu.Lock()
	defer resultMu.Unlock()
	if resultReceived == nil {
		t.Fatal("no result received")
	}
	if resultReceived.CallId != "call-42" {
		t.Errorf("call_id = %q, want call-42", resultReceived.CallId)
	}
	if resultReceived.Content != "hello from node" {
		t.Errorf("content = %q, want %q", resultReceived.Content, "hello from node")
	}
	if toolReceived == nil {
		t.Fatal("node did not receive tool request")
	}
	if toolReceived.ToolName != "bash" {
		t.Errorf("tool_name = %q, want bash", toolReceived.ToolName)
	}

	cancel()
	nodeWg.Wait()
}

func TestStream_mTLS_WrongServerKey(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	clientID := testIdentityFromSeed(1)
	approved := map[string]bool{nodeIDFromIdentity(clientID): true}
	_, addr, _, _ := setupApprovalTest(t, registry, approved)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}

	// Pin a WRONG public key — simulates a MITM or stale tracker data.
	wrongID := testIdentityFromSeed(99)
	clientTLS := cluster.ClientTLSConfig(clientCert, wrongID.PublicKey)

	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: addr,
		NodeName:   "wrong-key-node",
		TLSConfig:  clientTLS,
		Logger:     slog.Default(),
	})

	err = ws.Connect(ctx)
	if err == nil {
		t.Fatal("expected connection to fail with wrong server key")
	}

	if registry.Len() != 0 {
		t.Error("node should not have registered with wrong key")
	}
}

func TestStream_mTLS_NoPinning(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	clientID := testIdentityFromSeed(2)
	approved := map[string]bool{nodeIDFromIdentity(clientID): true}
	_, addr, _, _ := setupApprovalTest(t, registry, approved)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}

	// No pinning — nil expectedPubKey.
	clientTLS := cluster.ClientTLSConfig(clientCert, nil)

	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: addr,
		NodeName:   "no-pin-node",
		Capacity:   1,
		TLSConfig:  clientTLS,
		Logger:     slog.Default(),
	})

	var connectErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		connectErr = ws.Connect(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		nodes := registry.List()
		for _, n := range nodes {
			if n.Name == "no-pin-node" {
				cancel()
				wg.Wait()
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()
	if connectErr != nil {
		t.Fatalf("connect error: %v", connectErr)
	}
	t.Fatal("no-pin-node never appeared in registry")
}
