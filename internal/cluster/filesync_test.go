package cluster

import (
	"archive/tar"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

func TestShouldIgnore(t *testing.T) {
	// Uses default patterns (no .syncignore file in temp dir).
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  t.TempDir(),
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
	})

	tests := []struct {
		path   string
		ignore bool
	}{
		{"workspace/project/main.go", false},
		{"agents/operator/agent.md", false},
		{".git/objects/abc123", true},
		{"workspace/project/.git/HEAD", true},
		{"workspace/project/node_modules/pkg/index.js", true},
		{"workspace/.DS_Store", true},
		{"workspace/file.swp", true},
		{"workspace/file.go", false},
		{"vendor/pkg/lib.go", false},
		{"workspace/Thumbs.db", true},
	}

	for _, tt := range tests {
		if got := svc.shouldIgnore(tt.path); got != tt.ignore {
			t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignore)
		}
	}
}

func TestCreateAndApplyInitialSync(t *testing.T) {
	// Set up a source directory with some files.
	srcDir := t.TempDir()
	os.MkdirAll(filepath.Join(srcDir, "workspace", "project"), 0o755)
	os.MkdirAll(filepath.Join(srcDir, "agents", "helper"), 0o755)
	os.WriteFile(filepath.Join(srcDir, "workspace", "project", "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(srcDir, "agents", "helper", "agent.md"), []byte("# Helper"), 0o644)

	// Also create an ignored directory — it should not be synced.
	os.MkdirAll(filepath.Join(srcDir, "workspace", "project", ".git"), 0o755)
	os.WriteFile(filepath.Join(srcDir, "workspace", "project", ".git", "HEAD"), []byte("ref: refs/heads/main"), 0o644)

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

	if err := dst.ApplyInitialSyncStream(bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyInitialSyncStream: %v", err)
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
		Mode:    0o644,
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
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)
	os.WriteFile(filepath.Join(dir, "workspace", "file.txt"), []byte("old"), 0o644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	// Incoming change with a newer mtime should overwrite.
	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:           "workspace/file.txt",
		Content:        []byte("new"),
		Mode:           0o644,
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
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)

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
		Mode:           0o644,
		MtimeUnixNanos: firstMtime,
		OriginNode:     "leader",
	})

	// Simulate a local modification by writing directly and advancing mtime.
	filePath := filepath.Join(dir, "workspace", "file.txt")
	os.WriteFile(filePath, []byte("local version"), 0o644)
	now := time.Now()
	os.Chtimes(filePath, now, now)

	// Incoming change with mtime between the original sync and our local write.
	// This should trigger a conflict because local mtime > last received mtime.
	err := svc.ApplyFileUpdate(&pb.FileUpdate{
		Path:           "workspace/file.txt",
		Content:        []byte("remote version"),
		Mode:           0o644,
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
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)

	// Write a local file (simulating pre-existing content).
	filePath := filepath.Join(dir, "workspace", "file.txt")
	os.WriteFile(filePath, []byte("old"), 0o644)

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
		Mode:           0o644,
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
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)
	os.WriteFile(filepath.Join(dir, "workspace", "doomed.txt"), []byte("bye"), 0o644)

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

func TestApplyInitialSync_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "node-1",
	})

	// Create a zstd-compressed tar with a path-traversal entry.
	var buf bytes.Buffer
	zw, _ := zstd.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{
		Name:     "../../../etc/evil",
		Mode:     0o644,
		Size:     6,
		Typeflag: tar.TypeReg,
	})
	tw.Write([]byte("hacked"))
	tw.Close()
	zw.Close()

	// Apply as a streaming sync — the traversal entry should be silently skipped.
	r := bytes.NewReader(buf.Bytes())
	err := svc.ApplyInitialSyncStream(r)
	if err != nil {
		t.Fatalf("ApplyInitialSyncStream: %v", err)
	}

	// Verify the file was NOT created outside the root.
	if _, err := os.Stat(filepath.Join(dir, "..", "evil")); !os.IsNotExist(err) {
		t.Fatal("path traversal was not rejected: file exists outside root")
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
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)

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
	os.MkdirAll(subdir, 0o755)
	os.WriteFile(filepath.Join(subdir, "README.md"), []byte("hello"), 0o644)

	// Wait for debounce + processing.
	wantPath := filepath.Join("workspace", "new-project", "README.md")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := slices.Contains(sent, wantPath)
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

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Create file.
	if err := atomicWrite(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "hello" {
		t.Errorf("content = %q, want %q", string(content), "hello")
	}

	// Overwrite atomically.
	if err := atomicWrite(path, []byte("world"), 0o644); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	content, _ = os.ReadFile(path)
	if string(content) != "world" {
		t.Errorf("content = %q, want %q", string(content), "world")
	}

	// No temp files should remain.
	matches, _ := filepath.Glob(filepath.Join(dir, ".hiro-tmp-*"))
	if len(matches) != 0 {
		t.Errorf("expected temp files to be cleaned up, found %v", matches)
	}
}

func TestApplyInitialSyncStream(t *testing.T) {
	// Set up a source directory.
	srcDir := t.TempDir()
	os.MkdirAll(filepath.Join(srcDir, "workspace"), 0o755)
	os.WriteFile(filepath.Join(srcDir, "workspace", "file.go"), []byte("package main"), 0o644)

	src := NewFileSyncService(FileSyncConfig{
		RootDir:  srcDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "leader",
	})

	data, err := src.CreateInitialSync()
	if err != nil {
		t.Fatalf("CreateInitialSync: %v", err)
	}

	// Apply via streaming reader.
	dstDir := t.TempDir()
	dst := NewFileSyncService(FileSyncConfig{
		RootDir:  dstDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "worker-1",
	})

	if err := dst.ApplyInitialSyncStream(bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyInitialSyncStream: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dstDir, "workspace", "file.go"))
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("content = %q, want %q", string(content), "package main")
	}

	// No temp files should remain.
	matches, _ := filepath.Glob(filepath.Join(dstDir, "workspace", ".hiro-tmp-*"))
	if len(matches) != 0 {
		t.Errorf("expected no temp files, found %v", matches)
	}
}

func TestReconcile_CatchesDrift(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)
	os.WriteFile(filepath.Join(dir, "workspace", "original.txt"), []byte("v1"), 0o644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "leader",
	})

	// Take a snapshot (simulates CreateInitialSync).
	snap, err := svc.CreateInitialSync()
	if err != nil {
		t.Fatalf("CreateInitialSync: %v", err)
	}
	_ = snap // The tar was created with "v1" content.

	// Simulate drift: modify a file after the snapshot.
	os.WriteFile(filepath.Join(dir, "workspace", "original.txt"), []byte("v2"), 0o644)
	// Also add a new file.
	os.WriteFile(filepath.Join(dir, "workspace", "new.txt"), []byte("new"), 0o644)

	// Collect updates sent by Reconcile.
	var sent []*pb.FileUpdate
	svc.sendFn = func(u *pb.FileUpdate) error {
		sent = append(sent, u)
		return nil
	}

	if err := svc.Reconcile(nil); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Both files should be sent (nil knownFiles = send everything).
	if len(sent) < 2 {
		t.Fatalf("expected at least 2 updates, got %d", len(sent))
	}

	// Verify the modified file has the new content.
	foundModified := false
	foundNew := false
	for _, u := range sent {
		if u.Path == "workspace/original.txt" {
			foundModified = true
			if string(u.Content) != "v2" {
				t.Errorf("original.txt content = %q, want %q", string(u.Content), "v2")
			}
		}
		if u.Path == "workspace/new.txt" {
			foundNew = true
			if string(u.Content) != "new" {
				t.Errorf("new.txt content = %q, want %q", string(u.Content), "new")
			}
		}
	}
	if !foundModified {
		t.Error("Reconcile did not send update for modified original.txt")
	}
	if !foundNew {
		t.Error("Reconcile did not send update for new file new.txt")
	}
}

func TestShouldIgnore_HiveTmp(t *testing.T) {
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  t.TempDir(),
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
	})

	if !svc.shouldIgnore("workspace/.hiro-tmp-123456789") {
		t.Error("expected .hiro-tmp-* files to be ignored")
	}
	if svc.shouldIgnore("workspace/hiro-tmp-file.txt") {
		t.Error("regular files with hiro-tmp in name should not be ignored")
	}
}

func TestSyncIgnoreFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".syncignore"), []byte(`
# Custom ignore patterns
build
*.log
data/*.csv
workspace/logs
`), 0o644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
	})

	tests := []struct {
		path   string
		ignore bool
	}{
		{"workspace/build/output.js", true},    // "build" matches component
		{"workspace/app.log", true},            // "*.log" matches component
		{"data/export.csv", true},              // "data/*.csv" matches full path
		{"workspace/data/export.csv", false},   // "data/*.csv" doesn't match (different prefix)
		{"workspace/main.go", false},           // not matched
		{"workspace/.hiro-tmp-123", true},      // always ignored (hardcoded)
		{"workspace/node_modules/x.js", false}, // not in custom file
		{".git/HEAD", false},                   // not in custom file
		{"workspace/logs/app.log", true},       // "workspace/logs" matches as directory prefix
		{"workspace/logs/sub/debug.log", true}, // prefix match works recursively
		{"workspace/logs", true},               // exact match on the path itself
		{"other/logs/readme.txt", false},       // different prefix, no match
	}

	for _, tt := range tests {
		if got := svc.shouldIgnore(tt.path); got != tt.ignore {
			t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignore)
		}
	}
}

func TestSyncIgnoreFile_TrailingSlash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".syncignore"), []byte("output/\n"), 0o644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
	})

	if !svc.shouldIgnore("workspace/output/bundle.js") {
		t.Error("trailing slash pattern should still match directory component")
	}
}

func TestParseIgnorePatterns(t *testing.T) {
	logger := slog.Default()
	patterns := parseIgnorePatterns([]string{
		"node_modules",
		"*.swp",
		"workspace/data/*.csv",
		"trailing/",
		"",
	}, logger)

	if len(patterns) != 4 {
		t.Fatalf("expected 4 patterns, got %d", len(patterns))
	}
	if patterns[0].isPath {
		t.Error("node_modules should not be a path pattern")
	}
	if patterns[1].isPath {
		t.Error("*.swp should not be a path pattern")
	}
	if !patterns[2].isPath {
		t.Error("workspace/data/*.csv should be a path pattern")
	}
	if patterns[3].isPath {
		t.Error("trailing/ (stripped to 'trailing') should not be a path pattern")
	}
}

func TestParseIgnorePatterns_InvalidSkipped(t *testing.T) {
	logger := slog.Default()
	patterns := parseIgnorePatterns([]string{
		"valid",
		"[unclosed", // malformed glob — should be skipped
		"**/*.log",  // ** not supported — should be skipped
		"also-valid",
	}, logger)

	if len(patterns) != 2 {
		t.Fatalf("expected 2 valid patterns, got %d", len(patterns))
	}
	if patterns[0].pattern != "valid" {
		t.Errorf("patterns[0] = %q, want %q", patterns[0].pattern, "valid")
	}
	if patterns[1].pattern != "also-valid" {
		t.Errorf("patterns[1] = %q, want %q", patterns[1].pattern, "also-valid")
	}
}

func TestSyncIgnoreFile_EmptyFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	// Empty .syncignore (only comments/blanks).
	os.WriteFile(filepath.Join(dir, ".syncignore"), []byte(`
# This file is intentionally empty
`), 0o644)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
	})

	// Should still use defaults — .git and node_modules should be ignored.
	if !svc.shouldIgnore(".git/HEAD") {
		t.Error("empty .syncignore should fall back to defaults (.git)")
	}
	if !svc.shouldIgnore("workspace/node_modules/pkg/index.js") {
		t.Error("empty .syncignore should fall back to defaults (node_modules)")
	}
}

func TestSyncIgnoreFile_MissingUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	// No .syncignore file at all.
	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
	})

	if !svc.shouldIgnore(".git/HEAD") {
		t.Error("missing .syncignore should use defaults (.git)")
	}
	if !svc.shouldIgnore("workspace/file.swp") {
		t.Error("missing .syncignore should use defaults (*.swp)")
	}
}

func TestApplyInitialSync_IgnoredEntriesSkipped(t *testing.T) {
	// Create a tar with an ignored directory entry — it should be
	// skipped on the receiving side even though the sender included it.
	srcDir := t.TempDir()
	os.MkdirAll(filepath.Join(srcDir, "workspace", "project"), 0o755)
	os.WriteFile(filepath.Join(srcDir, "workspace", "project", "main.go"), []byte("package main"), 0o644)

	// Create initial sync without a .syncignore (defaults apply on sender).
	src := NewFileSyncService(FileSyncConfig{
		RootDir:  srcDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "leader",
	})
	data, err := src.CreateInitialSync()
	if err != nil {
		t.Fatalf("CreateInitialSync: %v", err)
	}

	// On the receiving side, create a .syncignore that excludes "project".
	dstDir := t.TempDir()
	os.WriteFile(filepath.Join(dstDir, ".syncignore"), []byte("project\n"), 0o644)

	dst := NewFileSyncService(FileSyncConfig{
		RootDir:  dstDir,
		SyncDirs: []string{"workspace"},
		NodeID:   "worker-1",
	})

	if err := dst.ApplyInitialSyncStream(bytes.NewReader(data)); err != nil {
		t.Fatalf("ApplyInitialSyncStream: %v", err)
	}

	// The "project" directory and its contents should NOT be extracted.
	if _, err := os.Stat(filepath.Join(dstDir, "workspace", "project", "main.go")); !os.IsNotExist(err) {
		t.Error("expected ignored 'project' directory to be excluded from initial sync extraction")
	}
}

func TestSyncIgnoreHotReload(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "workspace"), 0o755)

	svc := NewFileSyncService(FileSyncConfig{
		RootDir:  dir,
		SyncDirs: []string{"workspace"},
		NodeID:   "test",
		SendFn:   func(*pb.FileUpdate) error { return nil },
	})

	// Initially no .syncignore — defaults apply.
	if !svc.shouldIgnore("workspace/node_modules/x.js") {
		t.Fatal("expected defaults to ignore node_modules")
	}
	if svc.shouldIgnore("workspace/custom-build/output.js") {
		t.Fatal("expected defaults to not ignore custom-build")
	}

	// Write a .syncignore that adds custom-build but drops node_modules.
	os.WriteFile(filepath.Join(dir, ".syncignore"), []byte("custom-build\n"), 0o644)

	// Simulate hot-reload (normally triggered by fsnotify in WatchAndSync).
	svc.reloadSyncIgnore()

	if svc.shouldIgnore("workspace/node_modules/x.js") {
		t.Error("after reload, node_modules should no longer be ignored (not in custom file)")
	}
	if !svc.shouldIgnore("workspace/custom-build/output.js") {
		t.Error("after reload, custom-build should be ignored")
	}

	// Remove .syncignore — should revert to defaults.
	os.Remove(filepath.Join(dir, ".syncignore"))
	svc.reloadSyncIgnore()

	if !svc.shouldIgnore("workspace/node_modules/x.js") {
		t.Error("after removing .syncignore, defaults should apply again (node_modules)")
	}
	if svc.shouldIgnore("workspace/custom-build/output.js") {
		t.Error("after removing .syncignore, custom-build should not be ignored")
	}
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
