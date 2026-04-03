package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMemoryFile_NotExists(t *testing.T) {
	dir := t.TempDir()
	content, err := ReadMemoryFile(dir)
	if err != nil {
		t.Fatalf("ReadMemoryFile: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string, got %q", content)
	}
}

func TestWriteAndReadMemoryFile(t *testing.T) {
	dir := t.TempDir()
	want := "## Preferences\n- User likes YAML"

	if err := WriteMemoryFile(dir, want); err != nil {
		t.Fatalf("WriteMemoryFile: %v", err)
	}

	got, err := ReadMemoryFile(dir)
	if err != nil {
		t.Fatalf("ReadMemoryFile: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWriteMemoryFile_Overwrites(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMemoryFile(dir, "first version"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteMemoryFile(dir, "second version"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := ReadMemoryFile(dir)
	if err != nil {
		t.Fatalf("ReadMemoryFile: %v", err)
	}
	if got != "second version" {
		t.Errorf("expected overwrite, got %q", got)
	}
}

func TestWriteMemoryFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	WriteMemoryFile(dir, "secret stuff")

	info, err := os.Stat(filepath.Join(dir, "memory.md"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected 0o600 permissions, got %o", perm)
	}
}
