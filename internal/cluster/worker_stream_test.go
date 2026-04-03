package cluster

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

func TestWorkerStream_LeaderAddr(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "10.0.0.1:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	if got := ws.LeaderAddr(); got != "10.0.0.1:8081" {
		t.Fatalf("LeaderAddr() = %q, want %q", got, "10.0.0.1:8081")
	}
}

func TestWorkerStream_SetLeaderAddr(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "old:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	ws.SetLeaderAddr("new:9091")
	if got := ws.LeaderAddr(); got != "new:9091" {
		t.Fatalf("LeaderAddr() = %q, want %q", got, "new:9091")
	}
}

func TestWorkerStream_NodeID(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "10.0.0.1:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	// Before connecting, NodeID should be empty.
	if got := ws.NodeID(); got != "" {
		t.Fatalf("NodeID() = %q, want empty", got)
	}
}

func TestWorkerStream_Connect_NoTLS(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "10.0.0.1:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
		// No TLSConfig.
	})

	err := ws.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for missing TLS config")
	}
}

func TestWorkerStream_Send_NotConnected(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "10.0.0.1:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	err := ws.Send(&pb.NodeMessage{
		Msg: &pb.NodeMessage_Heartbeat{Heartbeat: &pb.NodeHeartbeat{}},
	})
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestWorkerStream_SetHandlers(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "10.0.0.1:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	// Verify handler setters don't panic.
	ws.SetSpawnHandler(func(_ context.Context, _ *pb.SpawnWorker) {})
	ws.SetExecuteToolHandler(func(_ context.Context, _ *pb.ExecuteToolRemote) {})
	ws.SetShutdownWorkerHandler(func(_ context.Context, _ *pb.ShutdownWorker) {})
	ws.SetKillWorkerHandler(func(_ context.Context, _ *pb.KillWorker) {})
	ws.SetFileSyncHandler(func(_ context.Context, _ *pb.FileSyncData) {})
	ws.SetOnConnected(func() {})
	ws.SetFileUpdateHandler(func(_ context.Context, _ *pb.FileUpdate) {})
	ws.SetCreateTerminalHandler(func(_ context.Context, _ *pb.CreateTerminal) {})
	ws.SetTerminalInputHandler(func(_ context.Context, _ *pb.TerminalInput) {})
	ws.SetTerminalResizeHandler(func(_ context.Context, _ *pb.TerminalResize) {})
	ws.SetCloseTerminalHandler(func(_ context.Context, _ *pb.CloseTerminal) {})

	// Verify handlers were set.
	if ws.onSpawnWorker == nil {
		t.Error("onSpawnWorker not set")
	}
	if ws.onExecuteTool == nil {
		t.Error("onExecuteTool not set")
	}
	if ws.onShutdownWorker == nil {
		t.Error("onShutdownWorker not set")
	}
	if ws.onKillWorker == nil {
		t.Error("onKillWorker not set")
	}
	if ws.onFileSync == nil {
		t.Error("onFileSync not set")
	}
	if ws.onConnected == nil {
		t.Error("onConnected not set")
	}
	if ws.onFileUpdate == nil {
		t.Error("onFileUpdate not set")
	}
	if ws.onCreateTerminal == nil {
		t.Error("onCreateTerminal not set")
	}
	if ws.onTerminalInput == nil {
		t.Error("onTerminalInput not set")
	}
	if ws.onTerminalResize == nil {
		t.Error("onTerminalResize not set")
	}
	if ws.onCloseTerminal == nil {
		t.Error("onCloseTerminal not set")
	}
}

func TestRunConcurrent_NilHandler(t *testing.T) {
	t.Parallel()

	// Should not panic.
	sem := make(chan struct{}, 1)
	runConcurrent[*pb.SpawnWorker](context.Background(), sem, nil, &pb.SpawnWorker{})

	// Semaphore should be empty (handler was not run).
	select {
	case <-sem:
		t.Fatal("semaphore should be empty for nil handler")
	default:
	}
}

func TestRunConcurrent_CallsHandler(t *testing.T) {
	t.Parallel()

	sem := make(chan struct{}, 4)
	var mu sync.Mutex
	var called bool

	handler := func(_ context.Context, msg *pb.SpawnWorker) {
		mu.Lock()
		called = true
		mu.Unlock()
	}

	runConcurrent(context.Background(), sem, handler, &pb.SpawnWorker{RequestId: "test"})

	// Wait for handler to complete.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := called
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("handler was not called")
}

func TestDispatchLeaderMessage_ConcurrentHandlers(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "10.0.0.1:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	var mu sync.Mutex
	var spawnCalled, executeCalled, fileUpdateCalled bool

	ws.SetSpawnHandler(func(_ context.Context, _ *pb.SpawnWorker) {
		mu.Lock()
		spawnCalled = true
		mu.Unlock()
	})
	ws.SetExecuteToolHandler(func(_ context.Context, _ *pb.ExecuteToolRemote) {
		mu.Lock()
		executeCalled = true
		mu.Unlock()
	})
	ws.SetFileUpdateHandler(func(_ context.Context, _ *pb.FileUpdate) {
		mu.Lock()
		fileUpdateCalled = true
		mu.Unlock()
	})

	sem := make(chan struct{}, maxConcurrentHandlers)
	ctx := context.Background()

	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_SpawnWorker{SpawnWorker: &pb.SpawnWorker{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_ExecuteTool{ExecuteTool: &pb.ExecuteToolRemote{}},
	})
	// FileUpdate runs inline (not concurrent).
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_FileUpdate{FileUpdate: &pb.FileUpdate{}},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		allDone := spawnCalled && executeCalled && fileUpdateCalled
		mu.Unlock()
		if allDone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if !spawnCalled {
		t.Error("spawn handler not called")
	}
	if !executeCalled {
		t.Error("execute handler not called")
	}
	if !fileUpdateCalled {
		t.Error("file update handler not called")
	}
}

func TestDispatchLeaderMessage_NilHandlers(t *testing.T) {
	t.Parallel()

	ws := NewWorkerStream(WorkerStreamConfig{
		LeaderAddr: "10.0.0.1:8081",
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	sem := make(chan struct{}, 4)
	ctx := context.Background()

	// All handlers nil — should not panic.
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_SpawnWorker{SpawnWorker: &pb.SpawnWorker{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_ExecuteTool{ExecuteTool: &pb.ExecuteToolRemote{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_ShutdownWorker{ShutdownWorker: &pb.ShutdownWorker{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_KillWorker{KillWorker: &pb.KillWorker{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_FileSync{FileSync: &pb.FileSyncData{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_FileUpdate{FileUpdate: &pb.FileUpdate{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_CreateTerminal{CreateTerminal: &pb.CreateTerminal{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_TerminalInput{TerminalInput: &pb.TerminalInput{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_TerminalResize{TerminalResize: &pb.TerminalResize{}},
	})
	ws.dispatchLeaderMessage(ctx, sem, &pb.LeaderMessage{
		Msg: &pb.LeaderMessage_CloseTerminal{CloseTerminal: &pb.CloseTerminal{}},
	})
}
