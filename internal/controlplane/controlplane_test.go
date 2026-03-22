package controlplane

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestLoadMissingFile(t *testing.T) {
	cp, err := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if len(cp.SecretNames()) != 0 {
		t.Errorf("expected no secrets, got %v", cp.SecretNames())
	}
}

func TestLoadExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `secrets:
  API_KEY: "sk-123"
  DB_URL: "postgres://localhost"
agents:
  researcher:
    tools: [read_file, grep]
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	names := cp.SecretNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(names))
	}
	if names[0] != "API_KEY" || names[1] != "DB_URL" {
		t.Errorf("unexpected secret names: %v", names)
	}

	tools, ok := cp.AgentTools("researcher")
	if !ok {
		t.Fatal("expected researcher policy to exist")
	}
	if len(tools) != 2 || tools[0] != "read_file" || tools[1] != "grep" {
		t.Errorf("unexpected tools: %v", tools)
	}

	_, ok = cp.AgentTools("coordinator")
	if ok {
		t.Error("expected no policy for coordinator")
	}
}

func TestSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	cp.SetSecret("TOKEN", "abc123")
	cp.SetAgentTools("worker", []string{"read_file", "grep"})

	if err := cp.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Reload and verify
	cp2, err := Load(path, testLogger())
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	names := cp2.SecretNames()
	if len(names) != 1 || names[0] != "TOKEN" {
		t.Errorf("expected [TOKEN], got %v", names)
	}

	tools, ok := cp2.AgentTools("worker")
	if !ok {
		t.Fatal("expected worker policy")
	}
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
}

func TestSaveSkipsEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cp, _ := Load(path, testLogger())
	if err := cp.Save(); err != nil {
		t.Fatal(err)
	}

	// File should not have been created
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected no file for empty config")
	}
}

func TestSecretCRUD(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetSecret("A", "1")
	cp.SetSecret("B", "2")
	if len(cp.SecretNames()) != 2 {
		t.Fatal("expected 2 secrets")
	}

	env := cp.SecretEnv()
	if len(env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(env))
	}

	cp.DeleteSecret("A")
	if len(cp.SecretNames()) != 1 {
		t.Error("expected 1 secret after delete")
	}

	cp.DeleteSecret("nonexistent") // no-op
}

func TestAgentToolsCRUD(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	_, ok := cp.AgentTools("worker")
	if ok {
		t.Error("expected no policy initially")
	}

	cp.SetAgentTools("worker", []string{"bash", "read_file"})
	tools, ok := cp.AgentTools("worker")
	if !ok || len(tools) != 2 {
		t.Errorf("expected 2 tools, got %v", tools)
	}

	cp.ClearAgentTools("worker")
	_, ok = cp.AgentTools("worker")
	if ok {
		t.Error("expected no policy after clear")
	}
}

func TestAllPolicies(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetAgentTools("a", []string{"bash"})
	cp.SetAgentTools("b", []string{"grep"})

	policies := cp.AllPolicies()
	if len(policies) != 2 {
		t.Fatalf("expected 2 policies, got %d", len(policies))
	}

	// Verify it's a copy — modifying shouldn't affect original
	delete(policies, "a")
	if _, ok := cp.AgentTools("a"); !ok {
		t.Error("deleting from returned map should not affect ControlPlane")
	}
}

// --- Command tests ---

func TestCommandSecretsSet(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/secrets set TOKEN=abc123")
	if err != nil {
		t.Fatal(err)
	}
	if result != `Secret "TOKEN" set.` {
		t.Errorf("unexpected result: %s", result)
	}

	names := cp.SecretNames()
	if len(names) != 1 || names[0] != "TOKEN" {
		t.Errorf("expected [TOKEN], got %v", names)
	}
}

func TestCommandSecretsSetSpaceForm(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/secrets set TOKEN abc123")
	if err != nil {
		t.Fatal(err)
	}
	if result != `Secret "TOKEN" set.` {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestCommandSecretsSetValueWithEquals(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	_, err := cp.HandleCommand("/secrets set DB=postgres://host?opt=1")
	if err != nil {
		t.Fatal(err)
	}

	env := cp.SecretEnv()
	found := false
	for _, e := range env {
		if e == "DB=postgres://host?opt=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected secret value with equals sign, got env: %v", env)
	}
}

func TestCommandSecretsRm(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetSecret("TOKEN", "x")

	result, err := cp.HandleCommand("/secrets rm TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if result != `Secret "TOKEN" removed.` {
		t.Errorf("unexpected result: %s", result)
	}
	if len(cp.SecretNames()) != 0 {
		t.Error("expected no secrets after rm")
	}
}

func TestCommandSecretsList(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetSecret("A", "1")
	cp.SetSecret("B", "2")

	result, err := cp.HandleCommand("/secrets list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "A") || !strings.Contains(result, "B") {
		t.Errorf("expected both secret names in output: %s", result)
	}
}

func TestCommandToolsSet(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/tools set researcher read_file,grep,glob")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "researcher") {
		t.Errorf("unexpected result: %s", result)
	}

	tools, ok := cp.AgentTools("researcher")
	if !ok {
		t.Fatal("expected policy")
	}
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %v", tools)
	}
}

func TestCommandToolsList(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetAgentTools("worker", []string{"bash"})

	result, err := cp.HandleCommand("/tools list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "worker") || !strings.Contains(result, "bash") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestCommandToolsListSpecific(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetAgentTools("worker", []string{"bash", "grep"})

	result, err := cp.HandleCommand("/tools list worker")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "bash") || !strings.Contains(result, "grep") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestCommandToolsRm(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetAgentTools("worker", []string{"bash"})

	_, err := cp.HandleCommand("/tools rm worker")
	if err != nil {
		t.Fatal(err)
	}

	_, ok := cp.AgentTools("worker")
	if ok {
		t.Error("expected no policy after rm")
	}
}

func TestCommandUnknown(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	_, err := cp.HandleCommand("/foobar baz")
	if err == nil {
		t.Error("expected error for unknown command")
	}
}

func TestCommandEmpty(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	_, err := cp.HandleCommand("/")
	if err == nil {
		t.Error("expected error for empty command")
	}
}

