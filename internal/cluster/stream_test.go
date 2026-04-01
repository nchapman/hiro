package cluster_test

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

func validToken(token string) string {
	if token == "valid-token" {
		return "test-worker"
	}
	return ""
}

// testIdentityFromSeed creates a deterministic identity for testing.
func testIdentityFromSeed(seed byte) *cluster.NodeIdentity {
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	priv := ed25519.NewKeyFromSeed(s)
	pub := priv.Public().(ed25519.PublicKey)
	return &cluster.NodeIdentity{PrivateKey: priv, PublicKey: pub, NodeID: "test-node"}
}

// setupClusterTest creates a bufconn-based leader gRPC server and returns
// a dialer function for worker clients to connect through.
func setupClusterTest(t *testing.T, registry *cluster.NodeRegistry) (*cluster.LeaderStream, func(context.Context, string) (net.Conn, error)) {
	t.Helper()
	logger := slog.Default()
	leader := cluster.NewLeaderStream(registry, validToken, logger)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	leader.Register(srv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	return leader, dialer
}

// setupClusterTestTLS creates a real TCP-based leader gRPC server with mTLS.
func setupClusterTestTLS(t *testing.T, registry *cluster.NodeRegistry) (*cluster.LeaderStream, string, *cluster.NodeIdentity) {
	t.Helper()
	logger := slog.Default()
	leader := cluster.NewLeaderStream(registry, validToken, logger)

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

	return leader, lis.Addr().String(), serverID
}

func connectWorker(t *testing.T, ctx context.Context, dialer func(context.Context, string) (net.Conn, error), name string) *cluster.WorkerStream {
	t.Helper()
	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: "passthrough:///bufconn",
		NodeName:   name,
		JoinToken:  "valid-token",
		Capacity:   4,
		Logger:     slog.Default(),
	})
	return ws
}

func TestStream_Registration(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	_, dialer := setupClusterTest(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ws := connectWorker(t, ctx, dialer, "test-node")

	// Connect in background — it blocks on the message loop.
	var connectErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Override the gRPC dial to use bufconn.
		conn, err := grpc.NewClient("passthrough:///bufconn",
			grpc.WithContextDialer(dialer),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			connectErr = err
			return
		}
		defer conn.Close()

		client := pb.NewClusterClient(conn)
		stream, err := client.NodeStream(ctx)
		if err != nil {
			connectErr = err
			return
		}

		// Send registration.
		err = stream.Send(&pb.NodeMessage{
			Msg: &pb.NodeMessage_Register{
				Register: &pb.NodeRegister{
					NodeName:  "test-node",
					JoinToken: "valid-token",
					Capacity:  4,
				},
			},
		})
		if err != nil {
			connectErr = err
			return
		}

		// Wait for confirmation.
		resp, err := stream.Recv()
		if err != nil {
			connectErr = err
			return
		}

		reg := resp.GetRegistered()
		if reg == nil {
			connectErr = err
			return
		}

		_ = ws // appease unused var
		if reg.NodeId == "" {
			t.Error("expected non-empty node ID")
		}

		// Cancel to exit.
		cancel()

		// Drain the stream.
		for {
			_, err := stream.Recv()
			if err != nil {
				break
			}
		}
	}()

	wg.Wait()
	if connectErr != nil {
		t.Fatalf("connection error: %v", connectErr)
	}

	// Verify node was registered.
	nodes := registry.List()
	found := false
	for _, n := range nodes {
		if n.Name == "test-node" {
			found = true
			if n.Capacity != 4 {
				t.Errorf("expected capacity 4, got %d", n.Capacity)
			}
		}
	}
	if !found {
		t.Error("test-node not found in registry")
	}
}

func TestStream_InvalidToken(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	_, dialer := setupClusterTest(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewClusterClient(conn)
	stream, err := client.NodeStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Send registration with bad token.
	err = stream.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_Register{
			Register: &pb.NodeRegister{
				NodeName:  "bad-node",
				JoinToken: "wrong-token",
				Capacity:  1,
			},
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Should get an error on recv (server closes stream).
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error for invalid token")
	}

	// Node should not be registered.
	if registry.Len() != 0 {
		t.Error("expected no nodes registered")
	}
}

func TestStream_ToolExecution(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	leader, dialer := setupClusterTest(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
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
				NodeName:  "tool-node",
				JoinToken: "valid-token",
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
				// Send result back.
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

	// Give the goroutine time to start reading.
	time.Sleep(50 * time.Millisecond)

	// Leader sends a tool execution request.
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

	// Wait for result.
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

// TestStream_mTLS_Registration tests the full registration flow over mTLS
// using real TCP connections (not bufconn). This exercises the same code
// path as production: TLS handshake → gRPC stream → registration → confirmation.
func TestStream_mTLS_Registration(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	leader, addr, serverID := setupClusterTestTLS(t, registry)
	_ = leader

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a worker identity and TLS cert.
	clientID := testIdentityFromSeed(1)
	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}

	// Connect with mTLS, pinning the server's public key.
	clientTLS := cluster.ClientTLSConfig(clientCert, serverID.PublicKey)

	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: addr,
		NodeName:   "mtls-node",
		JoinToken:  "valid-token",
		Capacity:   2,
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
			if n.Name == "mtls-node" {
				cancel() // success — stop the connection
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
	t.Fatal("mtls-node never appeared in registry")
}

// TestStream_mTLS_WrongServerKey verifies that a worker rejects a leader
// whose public key doesn't match what the tracker reported.
func TestStream_mTLS_WrongServerKey(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	_, addr, _ := setupClusterTestTLS(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clientID := testIdentityFromSeed(1)
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
		JoinToken:  "valid-token",
		TLSConfig:  clientTLS,
		Logger:     slog.Default(),
	})

	err = ws.Connect(ctx)
	if err == nil {
		t.Fatal("expected connection to fail with wrong server key")
	}

	// Should not have registered.
	if registry.Len() != 0 {
		t.Error("node should not have registered with wrong key")
	}
}

// TestStream_mTLS_NoPinning verifies that workers without pubkey pinning
// (direct leader_addr mode) still connect successfully over mTLS.
func TestStream_mTLS_NoPinning(t *testing.T) {
	registry := cluster.NewNodeRegistry()
	leader, addr, _ := setupClusterTestTLS(t, registry)
	_ = leader

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clientID := testIdentityFromSeed(2)
	clientCert, err := cluster.TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}

	// No pinning — nil expectedPubKey. Direct leader_addr mode.
	clientTLS := cluster.ClientTLSConfig(clientCert, nil)

	ws := cluster.NewWorkerStream(cluster.WorkerStreamConfig{
		LeaderAddr: addr,
		NodeName:   "no-pin-node",
		JoinToken:  "valid-token",
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
