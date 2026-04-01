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
	// Save and restore global state.
	origRoots := getAllowedRoots()
	defer SetAllowedRoots(origRoots)

	SetAllowedRoots([]string{"/hiro"})
	wd := "/hiro/workspace"

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative inside root", "agents/foo.md", false},
		{"absolute inside root", "/hiro/workspace/file.txt", false},
		{"root itself", "/hiro", false},
		{"absolute outside root", "/etc/hosts", true},
		{"absolute outside root /opt", "/opt/mise/shims/node", true},
		{"traversal escape", "../../etc/passwd", true},
		{"tmp directory", "/tmp/something", true},
		{"sibling instance dir", "/hiro/../etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := resolveAndConfine(wd, tt.path)
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
	origRoots := getAllowedRoots()
	defer SetAllowedRoots(origRoots)

	// No roots configured = no confinement (non-isolated mode).
	SetAllowedRoots(nil)

	resolved, err := resolveAndConfine("/hiro", "/etc/hosts")
	if err != nil {
		t.Fatalf("unexpected error with no roots: %v", err)
	}
	if resolved != "/etc/hosts" {
		t.Errorf("expected /etc/hosts, got %s", resolved)
	}
}

func TestResolveAndConfine_MultipleRoots(t *testing.T) {
	origRoots := getAllowedRoots()
	defer SetAllowedRoots(origRoots)

	SetAllowedRoots([]string{"/hiro", "/workspace"})

	// Allowed in first root.
	if _, err := resolveAndConfine("/hiro", "/hiro/agents/foo.md"); err != nil {
		t.Errorf("expected access to /hiro: %v", err)
	}

	// Allowed in second root.
	if _, err := resolveAndConfine("/hiro", "/workspace/project/file.txt"); err != nil {
		t.Errorf("expected access to /workspace: %v", err)
	}

	// Denied outside both.
	if _, err := resolveAndConfine("/hiro", "/etc/hosts"); err == nil {
		t.Error("expected error for /etc/hosts")
	}
}

func TestResolveAndConfine_SymlinkEscape(t *testing.T) {
	origRoots := getAllowedRoots()
	defer SetAllowedRoots(origRoots)

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
	_, err := resolveAndConfine(root, "escape_link")
	if err == nil {
		t.Fatal("expected error for symlink escaping allowed root")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink error, got: %v", err)
	}

	// A regular file inside the root should still work.
	realFile := filepath.Join(root, "legit.txt")
	os.WriteFile(realFile, []byte("ok"), 0644)

	resolved, err := resolveAndConfine(root, "legit.txt")
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
	if err := atomicWriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", string(data), "hello")
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("permissions = %o, want 0644", perm)
	}

	// Overwrite existing file.
	if err := atomicWriteFile(path, []byte("world"), 0644); err != nil {
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
