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
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customContent := []byte("custom")
	if err := os.WriteFile(filepath.Join(agentsDir, "agent.md"), customContent, 0o644); err != nil {
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

func TestInit_ConfigDirPermissions(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	if err := Init(dir, logger); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	configPath := filepath.Join(dir, "config")
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	// config/ should be restricted to owner only (0700).
	perm := info.Mode().Perm()
	if perm != 0o700 {
		t.Errorf("config dir perms = %04o, want 0700", perm)
	}
}

func TestInit_ConfigDirPermsTightened(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	// Pre-create config/ with overly permissive mode.
	configPath := filepath.Join(dir, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Init(dir, logger); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o700 {
		t.Errorf("config dir perms not tightened: got %04o, want 0700", perm)
	}
}

func TestInit_AllRequiredDirs(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	if err := Init(dir, logger); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	expected := []string{"agents", "config", "db", "instances", "skills", "workspace"}
	for _, name := range expected {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("directory %s does not exist: %v", name, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", name)
		}
	}
}

func TestInit_SeededAgentHasContent(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	if err := Init(dir, logger); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Walk the seeded agents directory and verify all .md files are non-empty.
	agentsDir := filepath.Join(dir, "agents")
	err := filepath.WalkDir(agentsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			t.Errorf("seeded file is empty: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking agents dir: %v", err)
	}
}

func TestLookupGroupGID_NonexistentGroup(t *testing.T) {
	// lookupGroupGID should return -1 for groups that don't exist.
	gid := lookupGroupGID("hiro-nonexistent-group-xyz-99999")
	if gid != -1 {
		t.Errorf("expected -1 for nonexistent group, got %d", gid)
	}
}

func TestSetCoordinatorDir_NegativeGID(t *testing.T) {
	// setCoordinatorDir should be a no-op when coordGID is negative.
	dir := t.TempDir()
	err := setCoordinatorDir(dir, "test", -1)
	if err != nil {
		t.Errorf("expected no-op for negative GID, got: %v", err)
	}
}
