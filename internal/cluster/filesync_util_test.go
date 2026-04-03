package cluster

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFromReader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Create file.
	if err := atomicWriteFromReader(path, bytes.NewReader([]byte("hello")), 0o644); err != nil {
		t.Fatalf("atomicWriteFromReader: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("content = %q, want %q", string(content), "hello")
	}

	// Overwrite atomically.
	if err := atomicWriteFromReader(path, bytes.NewReader([]byte("world")), 0o644); err != nil {
		t.Fatalf("atomicWriteFromReader: %v", err)
	}
	content, _ = os.ReadFile(path)
	if string(content) != "world" {
		t.Fatalf("content = %q, want %q", string(content), "world")
	}

	// No temp files should remain.
	matches, _ := filepath.Glob(filepath.Join(dir, ".hiro-tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("expected temp files to be cleaned up, found %v", matches)
	}
}

func TestAtomicWriteFromReader_EmptyContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := atomicWriteFromReader(path, bytes.NewReader(nil), 0o644); err != nil {
		t.Fatalf("atomicWriteFromReader: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if len(content) != 0 {
		t.Fatalf("expected empty content, got %d bytes", len(content))
	}
}

func TestAtomicWrite_CreatesParentDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "file.txt")

	// atomicWrite does NOT create parent dirs — it expects them.
	// Verify the error.
	err := atomicWrite(path, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("expected error when parent dir does not exist")
	}
}

func TestAtomicWrite_Permissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "perms.txt")

	if err := atomicWrite(path, []byte("secret"), 0o600); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// On macOS/Linux the permissions should match.
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want %o", got, 0o600)
	}
}
