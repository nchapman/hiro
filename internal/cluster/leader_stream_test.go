package cluster

import (
	"log/slog"
	"path/filepath"
	"testing"

	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

func TestLeaderStream_SetRelayAddr(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	// Set relay addr with valid host:port.
	ls.SetRelayAddr("127.0.0.1:9443")

	// Verify it was stored.
	ls.mu.Lock()
	addr := ls.relayAddr
	ips := ls.relayIPs
	ls.mu.Unlock()

	if addr != "127.0.0.1:9443" {
		t.Fatalf("relayAddr = %q, want %q", addr, "127.0.0.1:9443")
	}
	if !ips["127.0.0.1"] {
		t.Fatalf("expected relay IP 127.0.0.1 to be resolved, got %v", ips)
	}
}

func TestLeaderStream_SetRelayAddr_NoPort(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	// If SplitHostPort fails, it should still store the raw address.
	ls.SetRelayAddr("invalid-addr")

	ls.mu.Lock()
	addr := ls.relayAddr
	ls.mu.Unlock()

	if addr != "invalid-addr" {
		t.Fatalf("relayAddr = %q, want %q", addr, "invalid-addr")
	}
}

func TestLeaderStream_ConnectedNodes_Empty(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	nodes := ls.ConnectedNodes()
	if len(nodes) != 0 {
		t.Fatalf("expected 0 connected nodes, got %d", len(nodes))
	}
}

func TestLeaderStream_SendToNode_NotConnected(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	err := ls.SendToNode("nonexistent", &pb.LeaderMessage{})
	if err == nil {
		t.Fatal("expected error for non-connected node")
	}
}

func TestLeaderStream_DisconnectNode_NotConnected(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	// Should be a no-op, not panic.
	ls.DisconnectNode("nonexistent")
}

func TestLeaderStream_DetectConnectionType(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	// Without relay configured, everything is direct.
	via := ls.detectConnectionType("192.168.1.1:50000")
	if via != "direct" {
		t.Fatalf("expected 'direct' without relay, got %q", via)
	}

	// Set relay addr.
	ls.SetRelayAddr("10.0.0.100:9443")

	// Peer IP matching relay should be "relay".
	via = ls.detectConnectionType("10.0.0.100:50000")
	if via != "relay" {
		t.Fatalf("expected 'relay' for relay IP, got %q", via)
	}

	// Different peer IP should be "direct".
	via = ls.detectConnectionType("192.168.1.1:50000")
	if via != "direct" {
		t.Fatalf("expected 'direct' for non-relay IP, got %q", via)
	}
}

func TestLeaderStream_DispatchNodeMessage(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	var gotSpawnResult, gotToolResult, gotWorkerExited, gotFileUpdate bool
	var gotJobCompletion, gotTerminalCreated, gotTerminalOutput, gotTerminalExited bool

	handlers := &NodeHandlers{
		OnSpawnResult:     func(_ NodeID, _ *pb.SpawnResult) { gotSpawnResult = true },
		OnToolResult:      func(_ NodeID, _ *pb.NodeToolResult) { gotToolResult = true },
		OnWorkerExited:    func(_ NodeID, _ *pb.WorkerExited) { gotWorkerExited = true },
		OnFileUpdate:      func(_ NodeID, _ *pb.FileUpdate) { gotFileUpdate = true },
		OnJobCompletion:   func(_ NodeID, _ *pb.JobCompletionNotify) { gotJobCompletion = true },
		OnTerminalCreated: func(_ NodeID, _ *pb.TerminalCreated) { gotTerminalCreated = true },
		OnTerminalOutput:  func(_ NodeID, _ *pb.TerminalOutput) { gotTerminalOutput = true },
		OnTerminalExited:  func(_ NodeID, _ *pb.TerminalExited) { gotTerminalExited = true },
	}

	nodeID := NodeID("test-node")

	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_SpawnResult{SpawnResult: &pb.SpawnResult{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_ToolResult{ToolResult: &pb.NodeToolResult{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_WorkerExited{WorkerExited: &pb.WorkerExited{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_FileUpdate{FileUpdate: &pb.FileUpdate{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_JobCompletion{JobCompletion: &pb.JobCompletionNotify{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_TerminalCreated{TerminalCreated: &pb.TerminalCreated{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_TerminalOutput{TerminalOutput: &pb.TerminalOutput{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_TerminalExited{TerminalExited: &pb.TerminalExited{}}})
	// Heartbeat is handled by readLoop (Touch), not dispatched; should not panic.
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_Heartbeat{Heartbeat: &pb.NodeHeartbeat{}}})

	if !gotSpawnResult {
		t.Error("OnSpawnResult not called")
	}
	if !gotToolResult {
		t.Error("OnToolResult not called")
	}
	if !gotWorkerExited {
		t.Error("OnWorkerExited not called")
	}
	if !gotFileUpdate {
		t.Error("OnFileUpdate not called")
	}
	if !gotJobCompletion {
		t.Error("OnJobCompletion not called")
	}
	if !gotTerminalCreated {
		t.Error("OnTerminalCreated not called")
	}
	if !gotTerminalOutput {
		t.Error("OnTerminalOutput not called")
	}
	if !gotTerminalExited {
		t.Error("OnTerminalExited not called")
	}
}

func TestLeaderStream_DispatchNodeMessage_NilHandlers(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	// All nil handlers should not panic.
	handlers := &NodeHandlers{}
	nodeID := NodeID("test-node")

	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_SpawnResult{SpawnResult: &pb.SpawnResult{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_ToolResult{ToolResult: &pb.NodeToolResult{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_WorkerExited{WorkerExited: &pb.WorkerExited{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_FileUpdate{FileUpdate: &pb.FileUpdate{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_JobCompletion{JobCompletion: &pb.JobCompletionNotify{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_TerminalCreated{TerminalCreated: &pb.TerminalCreated{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_TerminalOutput{TerminalOutput: &pb.TerminalOutput{}}})
	ls.dispatchNodeMessage(nodeID, handlers, &pb.NodeMessage{Msg: &pb.NodeMessage_TerminalExited{TerminalExited: &pb.TerminalExited{}}})
}

func TestLeaderStream_SetOnNodeConnected(t *testing.T) {
	t.Parallel()

	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())

	called := false
	ls.SetOnNodeConnected(func(_ NodeID) { called = true })

	if ls.onNodeConnected == nil {
		t.Fatal("expected onNodeConnected to be set")
	}

	// The actual callback is invoked during NodeStream, which is tested in stream_test.go.
	_ = called
}
