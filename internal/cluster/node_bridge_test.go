package cluster

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nchapman/hiro/internal/ipc"
	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

func TestPrepareSpawnConfig(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	nb := &NodeBridge{
		rootDir: rootDir,
		workers: make(map[string]*localWorker),
	}

	msg := &pb.SpawnWorker{
		InstanceId:     "inst-1",
		SessionId:      "sess-1",
		AgentName:      "test-agent",
		EffectiveTools: map[string]bool{"Bash": true, "Read": true},
		WorkingDir:     "workspace",
		SessionDir:     "instances/inst-1/sessions/sess-1",
	}

	cfg, err := nb.prepareSpawnConfig(msg)
	if err != nil {
		t.Fatalf("prepareSpawnConfig: %v", err)
	}

	if cfg.InstanceID != "inst-1" {
		t.Fatalf("InstanceID = %q, want %q", cfg.InstanceID, "inst-1")
	}
	if cfg.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want %q", cfg.SessionID, "sess-1")
	}
	if cfg.AgentName != "test-agent" {
		t.Fatalf("AgentName = %q, want %q", cfg.AgentName, "test-agent")
	}
	if cfg.WorkingDir != filepath.Join(rootDir, "workspace") {
		t.Fatalf("WorkingDir = %q, want %q", cfg.WorkingDir, filepath.Join(rootDir, "workspace"))
	}

	expectedSessionDir := filepath.Join(rootDir, "instances/inst-1/sessions/sess-1")
	if cfg.SessionDir != expectedSessionDir {
		t.Fatalf("SessionDir = %q, want %q", cfg.SessionDir, expectedSessionDir)
	}

	// Session directory should have been created.
	if _, err := os.Stat(expectedSessionDir); os.IsNotExist(err) {
		t.Fatal("session directory was not created")
	}
	// Subdirectories should exist.
	for _, sub := range []string{"scratch", "tmp"} {
		subDir := filepath.Join(expectedSessionDir, sub)
		if _, err := os.Stat(subDir); os.IsNotExist(err) {
			t.Fatalf("subdirectory %q was not created", sub)
		}
	}
}

func TestPrepareSpawnConfig_EmptyWorkingDir(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	nb := &NodeBridge{
		rootDir: rootDir,
		workers: make(map[string]*localWorker),
	}

	msg := &pb.SpawnWorker{
		InstanceId: "inst-1",
		SessionId:  "sess-1",
		AgentName:  "test-agent",
		WorkingDir: "",
		SessionDir: "sessions/sess-1",
	}

	cfg, err := nb.prepareSpawnConfig(msg)
	if err != nil {
		t.Fatalf("prepareSpawnConfig: %v", err)
	}

	// Empty working dir should default to rootDir.
	if cfg.WorkingDir != rootDir {
		t.Fatalf("WorkingDir = %q, want %q (rootDir)", cfg.WorkingDir, rootDir)
	}
}

func TestPrepareSpawnConfig_DotWorkingDir(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	nb := &NodeBridge{
		rootDir: rootDir,
		workers: make(map[string]*localWorker),
	}

	msg := &pb.SpawnWorker{
		InstanceId: "inst-1",
		SessionId:  "sess-1",
		AgentName:  "test-agent",
		WorkingDir: ".",
		SessionDir: "sessions/sess-1",
	}

	cfg, err := nb.prepareSpawnConfig(msg)
	if err != nil {
		t.Fatalf("prepareSpawnConfig: %v", err)
	}

	if cfg.WorkingDir != rootDir {
		t.Fatalf("WorkingDir = %q, want %q (rootDir)", cfg.WorkingDir, rootDir)
	}
}

func TestCreateWorkerSocket(t *testing.T) {
	t.Parallel()

	nb := &NodeBridge{
		rootDir: t.TempDir(),
		workers: make(map[string]*localWorker),
	}

	socketDir, socketPath, err := nb.createWorkerSocket("sess-12345")
	if err != nil {
		t.Fatalf("createWorkerSocket: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(socketDir)
	})

	if socketDir == "" {
		t.Fatal("socketDir is empty")
	}
	if socketPath == "" {
		t.Fatal("socketPath is empty")
	}
	if socketPath != socketDir+"/a.sock" {
		t.Fatalf("socketPath = %q, want %q", socketPath, socketDir+"/a.sock")
	}

	// Socket directory should exist.
	if _, err := os.Stat(socketDir); os.IsNotExist(err) {
		t.Fatal("socket directory was not created")
	}
}

func TestCreateWorkerSocket_LongSessionID(t *testing.T) {
	t.Parallel()

	nb := &NodeBridge{
		rootDir: t.TempDir(),
		workers: make(map[string]*localWorker),
	}

	// Session ID longer than MaxSessionPrefix should be truncated.
	longID := "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz"
	socketDir, _, err := nb.createWorkerSocket(longID)
	if err != nil {
		t.Fatalf("createWorkerSocket: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(socketDir)
	})

	expected := "/tmp/hiro-" + longID[:ipc.MaxSessionPrefix]
	if socketDir != expected {
		t.Fatalf("socketDir = %q, want %q", socketDir, expected)
	}
}

func TestNodeBridge_WorkerMapStartsEmpty(t *testing.T) {
	t.Parallel()

	nb := &NodeBridge{
		rootDir: t.TempDir(),
		workers: make(map[string]*localWorker),
	}

	nb.mu.Lock()
	count := len(nb.workers)
	nb.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected empty worker map, got %d entries", count)
	}
}
