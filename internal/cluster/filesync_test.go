package cluster

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/nchapman/hivebot/internal/ipc/proto"
)

func TestShouldIgnore(t *testing.T) {
	tests := []struct {
		path   string
		ignore bool
	}{
		{"workspace/project/main.go", false},
		{"agents/coordinator/agent.md", false},
		{".git/objects/abc123", true},
		{"workspace/project/.git/HEAD", true},
		{"workspace/project/node_modules/pkg/index.js", true},
		{"workspace/.DS_Store", true},
		{"workspace/file.swp", true},
		{"workspace/file.go", false},
		{"vendor/pkg/lib.go", true},
	}

	for _, tt := range tests {
		if got := shouldIgnore(tt.path); got != tt.ignore {
			t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignore)
		}
	}
}

func TestCreateAndApplyInitialSync(t *testing.T) {
	// Set up a source directory with some files.
	srcDir := t.TempDir()
	os.MkdirAll(filepath.Join(srcDir, "workspace", "project"), 0755)
	os.MkdirAll(filepath.Join(srcDir, "agents", "helper"), 0755)
	os.WriteFile(filepath.Join(srcDir, "workspace", "project", "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(srcDir, "agents", "helper", "agent.md"), []byte("# Helper"), 0644)

	// Also create an ignored directory — it should not be synced.
	os.MkdirAll(filepath.Join(srcDir, "workspace", "project", ".git"), 0755)
	os.WriteFile(filepath.Join(srcDir, "workspace", "project", ".git", "HEAD"), []byte("ref: refs/heads/main"), 0644)

	src := NewFileSyncService(FileSyncConfig{
		RootDir:  srcDir,
		SyncDirs: []string{"workspace", "agents"},
		NodeID:   "leader",
	})

	data, err := src.CreateInitialSync()
	if err != nil {
		t.Fatalf("CreateInitialSync: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty tar.gz")
	}

	// Apply to a destination directory.
	dstDir := t.TempDir()
	dst := NewFileSyncService(FileSyncConfig{
		RootDir:  dstDir,
		SyncDirs: []string{"workspace", "agents"},
		NodeID:   "worker-1",
	})

	if err := dst.ApplyInitialSync(data); err != nil {
		t.Fatalf("ApplyInitialSync: %v", err)
	}

	// Verify files exist.
	content, err := os.ReadFile(filepath.Join(dstDir, "workspace", "project", "main.go"))
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("main.go content = %q, want %q", string(content), "package main")
	}

	content, err = os.ReadFile(filepath.Join(dstDir, "agents", "helper", "agent.md"))
	if err != nil {
		t.Fatalf("reading agent.md: %v", err)
	}
	if string(content) != "# Helper" {
		t.Errorf("agent.md content = %q, want %q", string(content), "# Helper")
	}

	// Verify .git was NOT synced.
	if _, err := os.Stat(filepath.Join(dstDir, "workspace", "project", ".git")); !os.IsNotExist(err) {
		t.Error("expected .git directory to be excluded from sync")
	}
}

func TestApplyFileUpdate_Create(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:    "workspace/new-file.txt",
		Content: []byte("hello world"),
		Mode:    0644,
	})
	if err != nil {
		t.Fatalf("ApplyFileUpdate: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "workspace", "new-file.txt"))
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("content = %q, want %q", string(content), "hello world")
	}
}

func TestApplyFileUpdate_Overwrite(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0755)
	os.WriteFile(filepath.Join(dir, "workspace", "file.txt"), []byte("old"), 0644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	// Incoming change with a newer mtime should overwrite.
	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:           "workspace/file.txt",
		Content:        []byte("new"),
		Mode:           0644,
		MtimeUnixNanos: time.Now().Add(1 * time.Second).UnixNano(),
	})
	if err != nil {
		t.Fatalf("ApplyFileUpdate: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(dir, "workspace", "file.txt"))
	if string(content) != "new" {
		t.Errorf("content = %q, want %q", string(content), "new")
	}
}

func TestApplyFileUpdate_Conflict(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0755)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	// First, receive a synced version (establishes the tracking baseline).
	firstMtime := time.Now().Add(-10 * time.Second).UnixNano()
	svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:           "workspace/file.txt",
		Content:        []byte("synced version"),
		Mode:           0644,
		MtimeUnixNanos: firstMtime,
		OriginNode:     "leader",
	})

	// Simulate a local modification by writing directly and advancing mtime.
	filePath := filepath.Join(dir, "workspace", "file.txt")
	os.WriteFile(filePath, []byte("local version"), 0644)
	now := time.Now()
	os.Chtimes(filePath, now, now)

	// Incoming change with mtime between the original sync and our local write.
	// This should trigger a conflict because local mtime > last received mtime.
	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:           "workspace/file.txt",
		Content:        []byte("remote version"),
		Mode:           0644,
		MtimeUnixNanos: time.Now().Add(-5 * time.Second).UnixNano(),
		OriginNode:     "node-2",
	})
	if err != nil {
		t.Fatalf("ApplyFileUpdate: %v", err)
	}

	// Local version should be preserved.
	content, _ := os.ReadFile(filePath)
	if string(content) != "local version" {
		t.Errorf("local file should be preserved, got %q", string(content))
	}

	// Conflict file should exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "workspace", "file.txt.conflict.*"))
	if len(matches) == 0 {
		t.Error("expected a conflict file to be created")
	} else {
		conflictContent, _ := os.ReadFile(matches[0])
		if string(conflictContent) != "remote version" {
			t.Errorf("conflict file content = %q, want %q", string(conflictContent), "remote version")
		}
	}
}

func TestApplyFileUpdate_NoConflictOnFirstReceive(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0755)

	// Write a local file (simulating pre-existing content).
	filePath := filepath.Join(dir, "workspace", "file.txt")
	os.WriteFile(filePath, []byte("old"), 0644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	// First sync of this file should NOT trigger a conflict even though
	// the local file exists — the file isn't tracked yet.
	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:           "workspace/file.txt",
		Content:        []byte("synced"),
		Mode:           0644,
		MtimeUnixNanos: time.Now().UnixNano(),
		OriginNode:     "leader",
	})
	if err != nil {
		t.Fatalf("ApplyFileUpdate: %v", err)
	}

	content, _ := os.ReadFile(filePath)
	if string(content) != "synced" {
		t.Errorf("content = %q, want %q", string(content), "synced")
	}

	// No conflict files should exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "workspace", "file.txt.conflict.*"))
	if len(matches) != 0 {
		t.Errorf("expected no conflict files, found %d", len(matches))
	}
}

func TestApplyFileUpdate_Delete(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0755)
	os.WriteFile(filepath.Join(dir, "workspace", "doomed.txt"), []byte("bye"), 0644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:    "workspace/doomed.txt",
		Deleted: true,
	})
	if err != nil {
		t.Fatalf("ApplyFileUpdate: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "workspace", "doomed.txt")); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestApplyFileUpdate_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:    "../../../etc/passwd",
		Content: []byte("hacked"),
	})
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestApplyFileUpdate_IgnoredPath(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	// Should silently skip ignored paths.
	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:    "workspace/project/.git/HEAD",
		Content: []byte("ref: refs/heads/main"),
	})
	if err != nil {
		t.Fatalf("expected no error for ignored path, got: %v", err)
	}

	// File should NOT have been created.
	if _, err := os.Stat(filepath.Join(dir, "workspace", "project", ".git", "HEAD")); !os.IsNotExist(err) {
		t.Error("expected ignored file to not be created")
	}
}

func TestEchoSuppression(t *testing.T) {
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  t.TempDir(),
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	svc.suppressEcho("workspace/file.txt")
	if !svc.isEchoSuppressed("workspace/file.txt") {
		t.Error("expected echo to be suppressed")
	}
	if svc.isEchoSuppressed("workspace/other.txt") {
		t.Error("expected different file to not be suppressed")
	}
}

// TestWatchAndSync_NewDirWithFile verifies that files written into a
// brand-new directory are detected by the watcher via scanNewDir, even
// when the file write happens before fsnotify registers the new directory.
func TestWatchAndSync_NewDirWithFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0755)

	var mu sync.Mutex
	var sent []string
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "test-node",
		SendFn: func(update *pb.FileUpdate) error {
			mu.Lock()
			sent = append(sent, update.Path)
			mu.Unlock()
			return nil
		},
	})

	go svc.WatchAndSync()
	defer svc.Stop()

	// Give the watcher time to start.
	time.Sleep(100 * time.Millisecond)

	// Create a new subdirectory with a file in a single burst.
	// This simulates the API upload race: mkdir + write before fsnotify
	// can register the new directory.
	subdir := filepath.Join(dir, "workspace", "new-project")
	os.MkdirAll(subdir, 0755)
	os.WriteFile(filepath.Join(subdir, "README.md"), []byte("hello"), 0644)

	// Wait for debounce + processing.
	wantPath := filepath.Join("workspace", "new-project", "README.md")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := false
		for _, p := range sent {
			if p == wantPath {
				found = true
				break
			}
		}
		mu.Unlock()
		if found {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	t.Errorf("expected file sync for %s, got: %v", wantPath, sent)
	mu.Unlock()
}

func TestSanitizeNodeID(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"node-1", "node-1"},
		{"gpu_box", "gpu_box"},
		{"node with spaces", "node_with_spaces"},
		{"node/slash", "node_slash"},
	}
	for _, tt := range tests {
		if got := sanitizeNodeID(tt.input); got != tt.want {
			t.Errorf("sanitizeNodeID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
