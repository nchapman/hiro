package cluster

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nchapman/hiro/internal/ipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

func newTestLeaderService(t *testing.T) *LeaderService {
	t.Helper()
	registry := NewNodeRegistry()
	pending := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"))
	ls := NewLeaderStream(registry, func(string) ApprovalStatus { return ApprovalPending }, pending, slog.Default())
	return NewLeaderService(ls, registry, slog.Default())
}

func TestLeaderService_DeliverToolResult(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	// Create a RemoteWorker and register it.
	rw := NewRemoteWorker(svc.stream, "node-1", "sess-1")
	svc.mu.Lock()
	svc.workers["sess-1"] = rw
	svc.mu.Unlock()

	// Set up a pending call.
	ch := make(chan toolResponse, 1)
	rw.mu.Lock()
	rw.pending["call-1"] = ch
	rw.mu.Unlock()

	// Deliver a result.
	svc.deliverToolResult(&pb.NodeToolResult{
		SessionId: "sess-1",
		CallId:    "call-1",
		Content:   "hello",
		IsError:   false,
	})

	resp := <-ch
	if resp.err != nil {
		t.Fatalf("unexpected error: %v", resp.err)
	}
	if resp.result.Content != "hello" {
		t.Fatalf("content = %q, want %q", resp.result.Content, "hello")
	}

	rw.Close()
}

func TestLeaderService_DeliverToolResult_WithError(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	rw := NewRemoteWorker(svc.stream, "node-1", "sess-1")
	svc.mu.Lock()
	svc.workers["sess-1"] = rw
	svc.mu.Unlock()

	ch := make(chan toolResponse, 1)
	rw.mu.Lock()
	rw.pending["call-1"] = ch
	rw.mu.Unlock()

	// Deliver a result with transport error.
	svc.deliverToolResult(&pb.NodeToolResult{
		SessionId: "sess-1",
		CallId:    "call-1",
		Content:   "",
		IsError:   true,
		Error:     "connection lost",
	})

	resp := <-ch
	if resp.err == nil {
		t.Fatal("expected transport error")
	}
	if resp.result.IsError != true {
		t.Fatal("expected IsError=true")
	}

	rw.Close()
}

func TestLeaderService_DeliverToolResult_NoWorker(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	// Should not panic when worker doesn't exist.
	svc.deliverToolResult(&pb.NodeToolResult{
		SessionId: "nonexistent",
		CallId:    "call-1",
		Content:   "orphan",
	})
}

func TestLeaderService_HandleWorkerExited(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	rw := NewRemoteWorker(svc.stream, "node-1", "sess-1")
	svc.mu.Lock()
	svc.workers["sess-1"] = rw
	svc.mu.Unlock()

	svc.handleWorkerExited(&pb.WorkerExited{
		SessionId: "sess-1",
		Error:     "crash",
	})

	// Worker should be removed.
	svc.mu.Lock()
	_, exists := svc.workers["sess-1"]
	svc.mu.Unlock()
	if exists {
		t.Fatal("worker should have been removed after exit")
	}

	// RemoteWorker should be closed.
	select {
	case <-rw.Done():
		// Good.
	default:
		t.Fatal("RemoteWorker Done channel should be closed")
	}
}

func TestLeaderService_HandleWorkerExited_NoWorker(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	// Should not panic.
	svc.handleWorkerExited(&pb.WorkerExited{
		SessionId: "nonexistent",
	})
}

func TestLeaderService_RemoveWorker(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	rw := NewRemoteWorker(svc.stream, "node-1", "sess-1")
	svc.mu.Lock()
	svc.workers["sess-1"] = rw
	svc.mu.Unlock()

	svc.removeWorker("sess-1")

	svc.mu.Lock()
	_, exists := svc.workers["sess-1"]
	svc.mu.Unlock()
	if exists {
		t.Fatal("worker should have been removed")
	}

	rw.Close()
}

func TestLeaderService_CleanupSpawn(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	rw := NewRemoteWorker(svc.stream, "node-1", "sess-1")
	ch := make(chan string, 1)
	svc.mu.Lock()
	svc.workers["sess-1"] = rw
	svc.spawnChans["req-1"] = ch
	svc.mu.Unlock()

	svc.cleanupSpawn("sess-1", "req-1")

	svc.mu.Lock()
	_, workerExists := svc.workers["sess-1"]
	_, chanExists := svc.spawnChans["req-1"]
	svc.mu.Unlock()

	if workerExists {
		t.Fatal("worker should have been removed")
	}
	if chanExists {
		t.Fatal("spawn channel should have been removed")
	}

	rw.Close()
}

func TestLeaderService_SetFileSync(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	if svc.getFileSync() != nil {
		t.Fatal("fileSync should be nil initially")
	}

	fs := NewFileSyncService(FileSyncConfig{
		RootDir:  t.TempDir(),
		SyncDirs: []string{"workspace"},
		NodeID:   "leader",
	})
	svc.SetFileSync(fs)

	if svc.getFileSync() != fs {
		t.Fatal("fileSync should be set")
	}
}

func TestLeaderService_KillWorkersOnNode(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	// Add workers on two different nodes.
	rw1 := NewRemoteWorker(svc.stream, "node-1", "sess-1")
	rw2 := NewRemoteWorker(svc.stream, "node-1", "sess-2")
	rw3 := NewRemoteWorker(svc.stream, "node-2", "sess-3")

	svc.mu.Lock()
	svc.workers["sess-1"] = rw1
	svc.workers["sess-2"] = rw2
	svc.workers["sess-3"] = rw3
	svc.mu.Unlock()

	// Kill workers on node-1.
	svc.KillWorkersOnNode("node-1")

	svc.mu.Lock()
	_, has1 := svc.workers["sess-1"]
	_, has2 := svc.workers["sess-2"]
	_, has3 := svc.workers["sess-3"]
	svc.mu.Unlock()

	if has1 {
		t.Fatal("sess-1 should have been removed")
	}
	if has2 {
		t.Fatal("sess-2 should have been removed")
	}
	if !has3 {
		t.Fatal("sess-3 should still exist (different node)")
	}

	// Verify killed workers are closed.
	select {
	case <-rw1.Done():
	default:
		t.Fatal("rw1 should be closed")
	}
	select {
	case <-rw2.Done():
	default:
		t.Fatal("rw2 should be closed")
	}

	rw3.Close()
}

func TestLeaderService_KillWorkersOnNode_NoWorkers(t *testing.T) {
	t.Parallel()

	svc := newTestLeaderService(t)

	// Should not panic.
	svc.KillWorkersOnNode("nonexistent-node")
}

func TestRemoteWorker_NodeID(t *testing.T) {
	t.Parallel()

	rw := NewRemoteWorker(nil, "node-42", "sess-1")
	if rw.NodeID() != "node-42" {
		t.Fatalf("NodeID() = %q, want %q", rw.NodeID(), "node-42")
	}
	rw.Close()
}

func TestRemoteWorker_Done(t *testing.T) {
	t.Parallel()

	rw := NewRemoteWorker(nil, "node-1", "sess-1")
	done := rw.Done()

	select {
	case <-done:
		t.Fatal("Done should not be closed yet")
	default:
	}

	rw.Close()

	select {
	case <-done:
		// Good.
	default:
		t.Fatal("Done should be closed after Close")
	}
}

func TestRemoteWorker_DoubleClose(t *testing.T) {
	t.Parallel()

	rw := NewRemoteWorker(nil, "node-1", "sess-1")
	rw.Close()
	// Second close should not panic.
	rw.Close()
}

func TestRemoteWorker_DeliverToolResult_NoMatchingCall(t *testing.T) {
	t.Parallel()

	rw := NewRemoteWorker(nil, "node-1", "sess-1")
	defer rw.Close()

	// Should not panic when no matching pending call.
	rw.DeliverToolResult("nonexistent-call", ipc.ToolResult{Content: "orphan"}, nil)
}
