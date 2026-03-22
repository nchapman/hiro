package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndReadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")

	want := Manifest{
		ID:        "abc-123",
		Agent:     "coordinator",
		Mode:      ModePersistent,
		ParentID:  "parent-456",
		CreatedAt: time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC),
	}

	if err := WriteManifest(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Agent != want.Agent {
		t.Errorf("Agent = %q, want %q", got.Agent, want.Agent)
	}
	if got.Mode != want.Mode {
		t.Errorf("Mode = %q, want %q", got.Mode, want.Mode)
	}
	if got.ParentID != want.ParentID {
		t.Errorf("ParentID = %q, want %q", got.ParentID, want.ParentID)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
}

func TestReadManifest_NotFound(t *testing.T) {
	_, err := ReadManifest("/nonexistent/manifest.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadManifest_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	os.WriteFile(path, []byte(":\t:bad\n\t\t:::"), 0644)

	_, err := ReadManifest(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestManifest_OmitsEmptyParentID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")

	m := Manifest{
		ID:        "abc",
		Agent:     "test",
		Mode:      ModePersistent,
		CreatedAt: time.Now(),
	}
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "parent_id") {
		t.Error("expected parent_id to be omitted when empty")
	}
}
