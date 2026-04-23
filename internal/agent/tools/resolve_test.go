package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	wd := "/home/agent/workspace"
	tests := []struct {
		name       string
		workingDir string
		path       string
		want       string
	}{
		{
			name:       "relative path",
			workingDir: wd,
			path:       "agents/foo/agent.md",
			want:       "/home/agent/workspace/agents/foo/agent.md",
		},
		{
			name:       "absolute path unchanged",
			workingDir: wd,
			path:       "/etc/hosts",
			want:       "/etc/hosts",
		},
		{
			name:       "dot-dot in relative path is cleaned",
			workingDir: wd,
			path:       "agents/../agents/foo.md",
			want:       "/home/agent/workspace/agents/foo.md",
		},
		{
			name:       "dot path resolves to workingDir",
			workingDir: wd,
			path:       ".",
			want:       "/home/agent/workspace",
		},
		{
			name:       "bare filename",
			workingDir: wd,
			path:       "file.txt",
			want:       "/home/agent/workspace/file.txt",
		},
		{
			name:       "nested relative path",
			workingDir: wd,
			path:       "a/b/c/d.txt",
			want:       "/home/agent/workspace/a/b/c/d.txt",
		},
		{
			name:       "empty workingDir falls back to relative",
			workingDir: "",
			path:       "file.txt",
			want:       "file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePath(tt.workingDir, tt.path)
			// Clean both for comparison since Join may clean
			if filepath.Clean(got) != filepath.Clean(tt.want) {
				t.Errorf("resolvePath(%q, %q) = %q, want %q", tt.workingDir, tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveAndConfine(t *testing.T) {
	restoreRoots(t)

	SetAllowedRoots([]string{"/home/hiro"})
	wd := "/home/hiro/workspace"

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative inside root", "agents/foo.md", false},
		{"absolute inside root", "/home/hiro/workspace/file.txt", false},
		{"root itself", "/home/hiro", false},
		{"absolute outside root", "/etc/hosts", true},
		{"absolute outside root /opt", "/opt/mise/shims/node", true},
		{"traversal escape", "../../etc/passwd", true},
		{"tmp directory", "/tmp/something", true},
		{"sibling instance dir", "/home/hiro/../etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := resolveForRead(wd, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for path %q, got resolved=%q", tt.path, resolved)
				} else if !strings.Contains(err.Error(), "access denied") {
					t.Errorf("expected 'access denied' error, got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for path %q: %v", tt.path, err)
				}
			}
		})
	}
}

func TestResolveAndConfine_NoRoots(t *testing.T) {
	restoreRoots(t)

	// No roots configured = no confinement (non-isolated mode).
	SetAllowedRoots(nil)

	resolved, err := resolveForRead("/home/hiro", "/etc/hosts")
	if err != nil {
		t.Fatalf("unexpected error with no roots: %v", err)
	}
	if resolved != "/etc/hosts" {
		t.Errorf("expected /etc/hosts, got %s", resolved)
	}
}

func TestResolveAndConfine_MultipleRoots(t *testing.T) {
	restoreRoots(t)

	SetAllowedRoots([]string{"/home/hiro", "/workspace"})

	// Allowed in first root.
	if _, err := resolveForRead("/home/hiro", "/home/hiro/agents/foo.md"); err != nil {
		t.Errorf("expected access to /hiro: %v", err)
	}

	// Allowed in second root.
	if _, err := resolveForRead("/home/hiro", "/workspace/project/file.txt"); err != nil {
		t.Errorf("expected access to /workspace: %v", err)
	}

	// Denied outside both.
	if _, err := resolveForRead("/home/hiro", "/etc/hosts"); err == nil {
		t.Error("expected error for /etc/hosts")
	}
}

func TestResolveAndConfine_SymlinkEscape(t *testing.T) {
	restoreRoots(t)

	// Create a temp directory as the "allowed root" and a sibling as the "escape target".
	root := t.TempDir()
	escape := t.TempDir()

	SetAllowedRoots([]string{root})

	// Create a symlink inside the root that points to the escape directory.
	link := filepath.Join(root, "escape_link")
	if err := os.Symlink(escape, link); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}

	// Accessing the symlink should be rejected — it resolves outside the root.
	_, err := resolveForRead(root, "escape_link")
	if err == nil {
		t.Fatal("expected error for symlink escaping allowed root")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink error, got: %v", err)
	}

	// A regular file inside the root should still work.
	realFile := filepath.Join(root, "legit.txt")
	os.WriteFile(realFile, []byte("ok"), 0o644)

	resolved, err := resolveForRead(root, "legit.txt")
	if err != nil {
		t.Fatalf("unexpected error for legit file: %v", err)
	}
	if resolved != realFile {
		t.Errorf("resolved = %q, want %q", resolved, realFile)
	}
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	// Write new file.
	if err := atomicWriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", string(data), "hello")
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("permissions = %o, want 0o644", perm)
	}

	// Overwrite existing file.
	if err := atomicWriteFile(path, []byte("world"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile overwrite: %v", err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "world" {
		t.Errorf("content after overwrite = %q, want %q", string(data), "world")
	}

	// No temp files should remain.
	matches, _ := filepath.Glob(filepath.Join(dir, ".hiro-tmp-*"))
	if len(matches) != 0 {
		t.Errorf("temp files not cleaned up: %v", matches)
	}
}

func TestAtomicWriteFile_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := atomicWriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(data))
	}
}

func TestAtomicWriteFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perms.txt")

	if err := atomicWriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 0o600", perm)
	}
}

func TestAtomicWriteFile_NonexistentDir(t *testing.T) {
	// Writing to a directory that doesn't exist should fail.
	err := atomicWriteFile("/nonexistent/dir/file.txt", []byte("x"), 0o644)
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestMkdirFor(t *testing.T) {
	// Empty or "." dir returns nil.
	if err := mkdirFor("file.txt"); err != nil {
		t.Errorf("mkdirFor bare file: %v", err)
	}

	// Nested dir creation.
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "file.txt")
	if err := mkdirFor(path); err != nil {
		t.Fatalf("mkdirFor nested: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b", "c")); err != nil {
		t.Errorf("expected directory to exist: %v", err)
	}
}

func TestResolveForWrite_RejectsReadOnlyPath(t *testing.T) {
	// The read/write split: a path that appears only in readableRoots
	// (RO-in-policy) must be rejected by resolveForWrite.
	restoreRoots(t)

	roRoot := t.TempDir()
	rwRoot := t.TempDir()
	SetReadableRoots([]string{roRoot, rwRoot})
	SetWritableRoots([]string{rwRoot})

	// Reads succeed on both roots.
	if _, err := resolveForRead(roRoot, "file.txt"); err != nil {
		t.Errorf("read should succeed on RO-only root: %v", err)
	}
	if _, err := resolveForRead(rwRoot, "file.txt"); err != nil {
		t.Errorf("read should succeed on RW root: %v", err)
	}

	// Writes fail on the RO-only root, succeed on the RW root.
	if _, err := resolveForWrite(roRoot, "file.txt"); err == nil {
		t.Errorf("write should be denied on RO-only root")
	}
	if _, err := resolveForWrite(rwRoot, "file.txt"); err != nil {
		t.Errorf("write should succeed on RW root: %v", err)
	}
}

func TestConfineTo_RejectsSymlinkedAncestor(t *testing.T) {
	// An agent could create a symlink earlier (workspace/link -> /etc) and
	// then write through it before the leaf file exists. EvalSymlinks on the
	// non-existent leaf returns an error; we must fall back to checking the
	// nearest existing ancestor's real path.
	root := t.TempDir()
	outside := t.TempDir()

	// Create a directory symlink inside root that escapes to outside.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := confineTo(root, "escape/newfile.txt", []string{root})
	if err == nil {
		t.Errorf("expected confineTo to reject write through symlinked ancestor")
	}
}

// restoreRoots snapshots both readable and writable atomics at call time and
// registers a t.Cleanup that restores them independently. Tests that mutate
// the globals via SetAllowedRoots/SetReadableRoots/SetWritableRoots should
// call this as their first line so they don't leak state into other tests.
func restoreRoots(t *testing.T) {
	t.Helper()
	r := loadRoots(&readableRoots)
	w := loadRoots(&writableRoots)
	t.Cleanup(func() {
		SetReadableRoots(r)
		SetWritableRoots(w)
	})
}

func TestExcludedDirs(t *testing.T) {
	expected := []string{"node_modules", "vendor", "dist", "__pycache__", ".git"}
	for _, name := range expected {
		if !excludedDirs[name] {
			t.Errorf("expected %q in excludedDirs", name)
		}
	}
}
