package platform

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestInit_EmptyPlatform(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	if err := Init(dir, logger); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Verify directory structure
	for _, d := range requiredDirs {
		path := filepath.Join(dir, d)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", d)
		}
	}

	// Verify coordinator was seeded with non-empty content
	coordAgent := filepath.Join(dir, "agents", "coordinator", "agent.md")
	data, err := os.ReadFile(coordAgent)
	if err != nil {
		t.Fatalf("expected coordinator agent.md to be seeded: %v", err)
	}
	if len(data) == 0 {
		t.Error("coordinator agent.md is empty")
	}

	// Verify coordinator skills were seeded
	skillFiles := []string{"create-agent.md", "create-skill.md", "delegate.md"}
	for _, f := range skillFiles {
		path := filepath.Join(dir, "agents", "coordinator", "skills", f)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("expected coordinator skill %s to be seeded: %v", f, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("coordinator skill %s is empty", f)
		}
	}
}

func TestInit_ExistingAgents(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	// Pre-create agents dir with a custom agent
	agentsDir := filepath.Join(dir, "agents", "my-agent")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	customContent := []byte("custom")
	if err := os.WriteFile(filepath.Join(agentsDir, "agent.md"), customContent, 0644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dir, logger); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Verify coordinator was NOT seeded (agents dir was non-empty)
	coordAgent := filepath.Join(dir, "agents", "coordinator", "agent.md")
	if _, err := os.Stat(coordAgent); !os.IsNotExist(err) {
		t.Errorf("expected coordinator to not be seeded when agents dir is non-empty")
	}

	// Verify custom agent was preserved
	data, err := os.ReadFile(filepath.Join(agentsDir, "agent.md"))
	if err != nil {
		t.Fatalf("custom agent.md missing after Init: %v", err)
	}
	if string(data) != "custom" {
		t.Errorf("custom agent.md content changed: got %q, want %q", string(data), "custom")
	}
}

func TestInit_Idempotent(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	if err := Init(dir, logger); err != nil {
		t.Fatalf("first Init() error: %v", err)
	}

	// Read seeded content before second Init
	coordAgent := filepath.Join(dir, "agents", "coordinator", "agent.md")
	before, err := os.ReadFile(coordAgent)
	if err != nil {
		t.Fatalf("coordinator agent.md missing after first Init: %v", err)
	}

	if err := Init(dir, logger); err != nil {
		t.Fatalf("second Init() error: %v", err)
	}

	// Verify seeded files survived unchanged
	after, err := os.ReadFile(coordAgent)
	if err != nil {
		t.Fatalf("coordinator agent.md missing after second Init: %v", err)
	}
	if string(before) != string(after) {
		t.Error("coordinator agent.md content changed after second Init")
	}
}
