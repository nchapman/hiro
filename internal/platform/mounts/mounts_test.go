package mounts

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDiscover_MissingDir(t *testing.T) {
	got, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestDiscover_FindsSubdirs(t *testing.T) {
	root := t.TempDir()
	mountsDir := filepath.Join(root, "mounts")
	if err := os.MkdirAll(filepath.Join(mountsDir, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mountsDir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Hidden and non-directories should be skipped.
	if err := os.MkdirAll(filepath.Join(mountsDir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountsDir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 mounts, got %d: %+v", len(got), got)
	}
	if got[0].Name != "archive" || got[1].Name != "photos" {
		t.Fatalf("expected sorted [archive, photos], got %+v", got)
	}
	for _, m := range got {
		if m.Mode != ModeRW {
			t.Errorf("mount %s: expected rw, got %s", m.Name, m.Mode)
		}
	}
}

func TestDiscover_ReadOnlyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits behave differently on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode checks")
	}
	root := t.TempDir()
	ro := filepath.Join(root, "mounts", "readonly")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	got, err := Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Mode != ModeRO {
		t.Fatalf("expected one ro mount, got %+v", got)
	}
}
