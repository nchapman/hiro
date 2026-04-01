package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPersonaFile_NotExists(t *testing.T) {
	dir := t.TempDir()
	content, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string, got %q", content)
	}
}

func TestWriteAndReadPersonaFile(t *testing.T) {
	dir := t.TempDir()
	want := "Friendly and precise. Prefers concise answers."

	if err := WritePersonaFile(dir, want); err != nil {
		t.Fatalf("WritePersonaFile: %v", err)
	}

	got, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWritePersonaFile_Overwrites(t *testing.T) {
	dir := t.TempDir()

	if err := WritePersonaFile(dir, "first version"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WritePersonaFile(dir, "second version"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := ReadPersonaFile(dir)
	if err != nil {
		t.Fatalf("ReadPersonaFile: %v", err)
	}
	if got != "second version" {
		t.Errorf("expected overwrite, got %q", got)
	}
}

func TestWritePersonaFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	WritePersonaFile(dir, "agent persona")

	info, err := os.Stat(filepath.Join(dir, "persona.md"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected 0600 permissions, got %o", perm)
	}
}
