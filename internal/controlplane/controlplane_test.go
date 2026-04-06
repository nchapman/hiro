package controlplane

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nchapman/hiro/internal/models"
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
    allowed_tools: [Read, Grep]
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
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
	if len(tools) != 2 || tools[0] != "Read" || tools[1] != "Grep" {
		t.Errorf("unexpected tools: %v", tools)
	}

	_, ok = cp.AgentTools("operator")
	if ok {
		t.Error("expected no policy for operator")
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
	cp.SetAgentTools("worker", []string{"Read", "Grep"})

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

	cp.SetAgentTools("worker", []string{"Bash", "Read"})
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
	cp.SetAgentTools("b", []string{"Grep"})

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

	result, err := cp.HandleCommand("/tools set researcher Read,Grep,Glob")
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
	cp.SetAgentTools("worker", []string{"Bash"})

	result, err := cp.HandleCommand("/tools list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "worker") || !strings.Contains(result, "Bash") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestCommandToolsListSpecific(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetAgentTools("worker", []string{"Bash", "Grep"})

	result, err := cp.HandleCommand("/tools list worker")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Bash") || !strings.Contains(result, "Grep") {
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

// --- Reload tests ---

func TestReload_ExternalEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n"), 0o600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.SecretNames()) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(cp.SecretNames()))
	}

	// Simulate external edit: add a second secret.
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n  B: \"2\"\n"), 0o600)
	if err := cp.Reload(); err != nil {
		t.Fatal(err)
	}

	names := cp.SecretNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 secrets after reload, got %d: %v", len(names), names)
	}
}

func TestReload_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n"), 0o600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Write invalid YAML (tabs are illegal in YAML).
	os.WriteFile(path, []byte("\t\tinvalid"), 0o600)
	if err := cp.Reload(); err != nil {
		t.Fatal("expected nil error for invalid YAML, got", err)
	}

	// State should be preserved.
	if len(cp.SecretNames()) != 1 {
		t.Errorf("expected state preserved after invalid YAML reload, got %d secrets", len(cp.SecretNames()))
	}
}

func TestReload_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n"), 0o600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Delete the file.
	os.Remove(path)
	if err := cp.Reload(); err != nil {
		t.Fatal("expected nil error for missing file, got", err)
	}

	// State should be preserved.
	if len(cp.SecretNames()) != 1 {
		t.Errorf("expected state preserved after missing file reload")
	}
}

func TestReload_PreservesSignerOnSamePassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$test\"\n  session_secret: \"0102030405060708091011121314151617181920212223242526272829303132\"\n"), 0o600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Force signer creation.
	signer, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}

	// Reload with same password hash — signer should be preserved.
	// Re-read the file since TokenSigner may have updated the session_secret.
	data, _ := os.ReadFile(path)
	os.WriteFile(path, data, 0o600)

	if err := cp.Reload(); err != nil {
		t.Fatal(err)
	}

	signer2, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}
	if signer != signer2 {
		t.Error("expected same signer instance after reload with unchanged password")
	}
}

func TestReload_InvalidatesSignerOnPasswordChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$original\"\n  session_secret: \"abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234\"\n"), 0o600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Force signer creation.
	signer, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}
	_ = signer

	// Reload with different password hash.
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$changed\"\n  session_secret: \"abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234\"\n"), 0o600)
	if err := cp.Reload(); err != nil {
		t.Fatal(err)
	}

	// Signer should have been invalidated — a new call should return a different instance.
	signer2, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}
	if signer2 == signer {
		t.Error("expected new signer instance after password change reload")
	}
}

func TestReload_InvalidatesSignerOnSecretRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$same\"\n  session_secret: \"aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd\"\n"), 0o600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	signer, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}

	// Rotate session_secret only (password hash unchanged).
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$same\"\n  session_secret: \"11223344112233441122334411223344112233441122334411223344aabbccdd\"\n"), 0o600)
	if err := cp.Reload(); err != nil {
		t.Fatal(err)
	}

	signer2, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}
	if signer2 == signer {
		t.Error("expected new signer instance after session secret rotation")
	}
}

func TestHandleCommand_SavesToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cp, _ := Load(path, testLogger())

	_, err := cp.HandleCommand("/secrets set TOKEN=secret123")
	if err != nil {
		t.Fatal(err)
	}

	// Verify config.yaml was written.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config.yaml should exist after mutation: %v", err)
	}
	if !strings.Contains(string(data), "TOKEN") {
		t.Errorf("config.yaml should contain 'TOKEN', got: %s", string(data))
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

// --- Provider tests ---

func TestProviderCRUD(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	// Initially no providers.
	if cp.IsConfigured() {
		t.Error("expected not configured initially")
	}

	// Set a provider.
	if err := cp.SetProvider("anthropic", ProviderConfig{APIKey: "sk-ant-123"}); err != nil {
		t.Fatal(err)
	}
	if !cp.IsConfigured() {
		t.Error("expected configured after SetProvider")
	}

	// Get it back.
	p, ok := cp.GetProvider("anthropic")
	if !ok || p.APIKey != "sk-ant-123" {
		t.Errorf("unexpected provider: %+v, ok=%v", p, ok)
	}

	// Missing provider.
	_, ok = cp.GetProvider("openrouter")
	if ok {
		t.Error("expected no openrouter provider")
	}

	// Delete clears default model if matching provider.
	cp.SetDefaultModelSpec(models.ModelSpec{Provider: "anthropic", Model: "test"})
	cp.DeleteProvider("anthropic")
	if !cp.DefaultModelSpec().IsEmpty() {
		t.Error("expected default cleared after deleting matching provider")
	}
	if cp.IsConfigured() {
		t.Error("expected not configured after delete")
	}
}

func TestProviderInfo_DefaultProvider(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetProvider("anthropic", ProviderConfig{APIKey: "sk-ant-111"})
	cp.SetProvider("openrouter", ProviderConfig{APIKey: "sk-or-222"})
	cp.SetDefaultModelSpec(models.ModelSpec{Provider: "openrouter", Model: "test-model"})

	pType, apiKey, _, ok := cp.ProviderInfo()
	if !ok || pType != "openrouter" || apiKey != "sk-or-222" {
		t.Errorf("expected openrouter as default, got type=%s key=%s ok=%v", pType, apiKey, ok)
	}
}

func TestProviderInfo_AlphabeticFallback(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetProvider("openrouter", ProviderConfig{APIKey: "sk-or-222"})
	cp.SetProvider("anthropic", ProviderConfig{APIKey: "sk-ant-111"})
	// No default set — should fall back to alphabetically first.

	pType, _, _, ok := cp.ProviderInfo()
	if !ok || pType != "anthropic" {
		t.Errorf("expected anthropic as alphabetic fallback, got %s", pType)
	}
}

func TestProviderInfo_NoProviders(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	_, _, _, ok := cp.ProviderInfo()
	if ok {
		t.Error("expected ok=false with no providers")
	}
}

func TestProviderByType(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetProvider("anthropic", ProviderConfig{APIKey: "sk-123", BaseURL: "https://custom.api"})

	apiKey, baseURL, ok := cp.ProviderByType("anthropic")
	if !ok || apiKey != "sk-123" || baseURL != "https://custom.api" {
		t.Errorf("unexpected: key=%s url=%s ok=%v", apiKey, baseURL, ok)
	}

	_, _, ok = cp.ProviderByType("missing")
	if ok {
		t.Error("expected ok=false for missing provider")
	}
}

func TestConfiguredProviderTypes(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetProvider("openrouter", ProviderConfig{APIKey: "sk-or"})
	cp.SetProvider("anthropic", ProviderConfig{APIKey: "sk-ant"})

	types := cp.ConfiguredProviderTypes()
	if len(types) != 2 || types[0] != "anthropic" || types[1] != "openrouter" {
		t.Errorf("expected sorted [anthropic, openrouter], got %v", types)
	}
}

func TestListProviders_MaskedKeys(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetProvider("anthropic", ProviderConfig{APIKey: "sk-ant-api03-longenoughkey"})

	providers := cp.ListProviders()
	p := providers["anthropic"]
	if p.APIKey == "sk-ant-api03-longenoughkey" {
		t.Error("expected masked key, got original")
	}
	if !strings.Contains(p.APIKey, "...") {
		t.Errorf("expected masked key to contain '...', got %s", p.APIKey)
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"short", "*****"},
		{"12345678", "********"},
		{"1234567890", "**********"},     // 10 chars: prefix+suffix would reveal all, so fully masked
		{"12345678901", "123456...8901"}, // 11 chars: just enough to mask 1 middle char
		{"sk-ant-api03-longkey", "sk-ant...gkey"},
	}
	for _, tt := range tests {
		got := maskKey(tt.input)
		if got != tt.want {
			t.Errorf("maskKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSetProvider_Validation(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if err := cp.SetProvider("", ProviderConfig{APIKey: "key"}); err == nil {
		t.Error("expected error for empty provider type")
	}
	if err := cp.SetProvider("anthropic", ProviderConfig{APIKey: ""}); err == nil {
		t.Error("expected error for empty API key")
	}
	if err := cp.SetProvider("anthropic", ProviderConfig{APIKey: "key"}); err != nil {
		t.Errorf("unexpected error for valid provider: %v", err)
	}
}

// --- Cluster command tests ---

func TestCommandClusterStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cp, _ := Load(path, testLogger())

	result, err := cp.HandleCommand("/cluster")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "not configured") {
		t.Errorf("unexpected result: %s", result)
	}

	// Set mode and verify status output.
	cp.SetClusterMode("leader")
	cp.SetClusterSwarmCode("test-1234")
	result, err = cp.HandleCommand("/cluster")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "leader") || !strings.Contains(result, "test-1234") {
		t.Errorf("unexpected result: %s", result)
	}
}

// --- Cluster config tests ---

func TestApproveNode(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	// Initially no approved nodes.
	if cp.IsNodeApproved("node-abc") {
		t.Error("node should not be approved initially")
	}

	// Approve a node.
	cp.ApproveNode("node-abc", "worker-1")
	if !cp.IsNodeApproved("node-abc") {
		t.Error("node should be approved after ApproveNode")
	}

	// Check the returned map.
	nodes := cp.ApprovedNodes()
	if n, ok := nodes["node-abc"]; !ok {
		t.Error("expected node in approved map")
	} else if n.Name != "worker-1" {
		t.Errorf("name = %q, want worker-1", n.Name)
	}

	// Revoke and verify.
	cp.RevokeNode("node-abc")
	if cp.IsNodeApproved("node-abc") {
		t.Error("node should not be approved after revocation")
	}
	if !cp.IsNodeRevoked("node-abc") {
		t.Error("node should be in revoked list after revocation")
	}

	// Clear revocation and verify.
	cp.ClearRevokedNode("node-abc")
	if cp.IsNodeRevoked("node-abc") {
		t.Error("node should not be revoked after clearing")
	}
}

func TestApprovedNodes_Copy(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.ApproveNode("a", "node-a")

	nodes := cp.ApprovedNodes()
	nodes["b"] = ApprovedNode{Name: "node-b"} // modify the copy

	// Original should be unaffected.
	nodes2 := cp.ApprovedNodes()
	if _, ok := nodes2["b"]; ok {
		t.Error("modifying returned map should not affect ControlPlane")
	}
}

func TestApprovedNodes_NilWhenEmpty(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	nodes := cp.ApprovedNodes()
	if nodes != nil {
		t.Errorf("expected nil for no approved nodes, got %v", nodes)
	}
}

func TestClusterMode_Default(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if mode := cp.ClusterMode(); mode != "" {
		t.Errorf("expected empty default (unconfigured), got %q", mode)
	}

	cp.SetClusterMode("worker")
	if mode := cp.ClusterMode(); mode != "worker" {
		t.Errorf("expected 'worker', got %q", mode)
	}
}

func TestClusterMode_EnvOverride(t *testing.T) {
	t.Setenv("HIRO_MODE", "worker")
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetClusterMode("leader")

	if mode := cp.ClusterMode(); mode != "worker" {
		t.Errorf("expected HIRO_MODE env var to take precedence, got %q", mode)
	}
}

func TestClusterTrackerURL_EnvOverride(t *testing.T) {
	t.Setenv("HIRO_TRACKER_URL", "https://env-tracker.example.com")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("cluster:\n  tracker_url: https://config-tracker.example.com\n"), 0o600)

	cp, _ := Load(path, testLogger())
	if url := cp.ClusterTrackerURL(); url != "https://env-tracker.example.com" {
		t.Errorf("expected env var to take precedence, got %q", url)
	}
}

// --- Error path tests ---

func TestHandleCommand_SaveWarning(t *testing.T) {
	// Point config path to a directory (not a file) to force Save failure.
	dir := t.TempDir()
	badPath := filepath.Join(dir, "nowrite", "config.yaml")
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.path = badPath

	// Create the parent dir as a file to block MkdirAll.
	os.WriteFile(filepath.Join(dir, "nowrite"), []byte("not a dir"), 0o600)

	result, err := cp.HandleCommand("/secrets set TOKEN=abc123")
	if err != nil {
		t.Fatal(err)
	}
	// In-memory state should be set.
	names := cp.SecretNames()
	if len(names) != 1 || names[0] != "TOKEN" {
		t.Errorf("expected secret set in memory, got %v", names)
	}
	// Result should contain warning about save failure.
	if !strings.Contains(result, "Warning") || !strings.Contains(result, "failed to save") {
		t.Errorf("expected save warning in result, got: %s", result)
	}
}

// --- Auth getter/setter tests ---

func TestAuth_NeedsSetup(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if !cp.NeedsSetup() {
		t.Error("expected NeedsSetup=true initially")
	}
	cp.SetPasswordHash("$2a$10$hash")
	if !cp.NeedsSetup() {
		t.Error("expected NeedsSetup=true with password but no mode")
	}
	cp.SetClusterMode("standalone")
	if cp.NeedsSetup() {
		t.Error("expected NeedsSetup=false after password + mode")
	}
}

func TestAuth_PasswordHash(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if cp.PasswordHash() != "" {
		t.Error("expected empty hash initially")
	}
	cp.SetPasswordHash("$2a$10$testhash")
	if cp.PasswordHash() != "$2a$10$testhash" {
		t.Errorf("unexpected hash: %s", cp.PasswordHash())
	}
}

func TestAuth_SetPasswordHash_InvalidatesSigner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$original\"\n  session_secret: \"aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd\"\n"), 0o600)

	cp, _ := Load(path, testLogger())
	signer1, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}

	cp.SetPasswordHash("$2a$10$changed")
	signer2, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}
	if signer1 == signer2 {
		t.Error("expected new signer after password change")
	}
}

func TestPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cp, _ := Load(path, testLogger())
	if cp.Path() != path {
		t.Errorf("Path() = %s, want %s", cp.Path(), path)
	}
}

// --- Cluster getter tests ---

func TestClusterGetters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`cluster:
  mode: worker
  leader_addr: "leader:9090"
  node_name: "node-1"
  swarm_code: "swarm42"
`), 0o600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	if v := cp.ClusterLeaderAddr(); v != "leader:9090" {
		t.Errorf("ClusterLeaderAddr() = %q", v)
	}
	if v := cp.ClusterNodeName(); v != "node-1" {
		t.Errorf("ClusterNodeName() = %q", v)
	}
	if v := cp.ClusterSwarmCode(); v != "swarm42" {
		t.Errorf("ClusterSwarmCode() = %q", v)
	}
}

func TestClusterSwarmCode_EnvOverride(t *testing.T) {
	t.Setenv("HIRO_SWARM_CODE", "env-swarm")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("cluster:\n  swarm_code: config-swarm\n"), 0o600)

	cp, _ := Load(path, testLogger())
	if v := cp.ClusterSwarmCode(); v != "env-swarm" {
		t.Errorf("expected env var to win, got %q", v)
	}
}

// --- Provider getter tests ---

func TestDefaultModelSpec(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if !cp.DefaultModelSpec().IsEmpty() {
		t.Error("expected empty default model spec")
	}
	cp.SetDefaultModelSpec(models.ModelSpec{Provider: "anthropic", Model: "claude-3-opus"})
	spec := cp.DefaultModelSpec()
	if spec.Provider != "anthropic" || spec.Model != "claude-3-opus" {
		t.Errorf("unexpected: %v", spec)
	}
	if spec.String() != "anthropic/claude-3-opus" {
		t.Errorf("unexpected string: %s", spec.String())
	}
}

// --- Deny tools tests ---

func TestAgentDisallowedToolsCRUD(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if dt := cp.AgentDisallowedTools("worker"); len(dt) != 0 {
		t.Errorf("expected no deny tools initially, got %v", dt)
	}

	cp.SetAgentDisallowedTools("worker", []string{"Bash(rm *)", "Bash(sudo *)"})
	dt := cp.AgentDisallowedTools("worker")
	if len(dt) != 2 || dt[0] != "Bash(rm *)" {
		t.Errorf("expected 2 deny tools, got %v", dt)
	}

	// Allow tools should be independent.
	cp.SetAgentTools("worker", []string{"Bash", "Read"})
	dt = cp.AgentDisallowedTools("worker")
	if len(dt) != 2 {
		t.Errorf("SetAgentTools should not affect deny tools, got %v", dt)
	}

	// Clear deny tools preserves allow.
	cp.ClearAgentDisallowedTools("worker")
	if dt := cp.AgentDisallowedTools("worker"); len(dt) != 0 {
		t.Errorf("expected no deny tools after clear, got %v", dt)
	}
	tools, ok := cp.AgentTools("worker")
	if !ok || len(tools) != 2 {
		t.Errorf("allow tools should survive ClearAgentDisallowedTools, got %v ok=%v", tools, ok)
	}
}

func TestSetAgentTools_PreservesDisallowedTools(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetAgentDisallowedTools("worker", []string{"Bash(rm *)"})
	cp.SetAgentTools("worker", []string{"Bash"})

	dt := cp.AgentDisallowedTools("worker")
	if len(dt) != 1 || dt[0] != "Bash(rm *)" {
		t.Errorf("deny tools should be preserved by SetAgentTools, got %v", dt)
	}
}

func TestClearAgentTools_PreservesDisallowedTools(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetAgentTools("worker", []string{"Bash"})
	cp.SetAgentDisallowedTools("worker", []string{"Bash(rm *)"})

	cp.ClearAgentTools("worker")

	// Policy should still exist because deny tools remain.
	dt := cp.AgentDisallowedTools("worker")
	if len(dt) != 1 {
		t.Errorf("deny tools should survive ClearAgentTools, got %v", dt)
	}
}

func TestDisallowedToolsSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cp, _ := Load(path, testLogger())
	cp.SetAgentTools("worker", []string{"Bash", "Read"})
	cp.SetAgentDisallowedTools("worker", []string{"Bash(rm *)"})
	if err := cp.Save(); err != nil {
		t.Fatal(err)
	}

	cp2, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	dt := cp2.AgentDisallowedTools("worker")
	if len(dt) != 1 || dt[0] != "Bash(rm *)" {
		t.Errorf("deny tools should roundtrip, got %v", dt)
	}
	tools, ok := cp2.AgentTools("worker")
	if !ok || len(tools) != 2 {
		t.Errorf("allow tools should roundtrip, got %v", tools)
	}
}

func TestCommandToolsDeny(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/tools deny researcher Bash(rm *),Bash(sudo *)")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "researcher") {
		t.Errorf("unexpected result: %s", result)
	}

	dt := cp.AgentDisallowedTools("researcher")
	if len(dt) != 2 {
		t.Errorf("expected 2 deny tools, got %v", dt)
	}
}

func TestCommandToolsRm_ClearsBoth(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetAgentTools("worker", []string{"Bash"})
	cp.SetAgentDisallowedTools("worker", []string{"Bash(rm *)"})

	_, err := cp.HandleCommand("/tools rm worker")
	if err != nil {
		t.Fatal(err)
	}

	_, ok := cp.AgentTools("worker")
	if ok {
		t.Error("expected no allow tools after rm")
	}
	if dt := cp.AgentDisallowedTools("worker"); len(dt) != 0 {
		t.Errorf("expected no deny tools after rm, got %v", dt)
	}
}

// --- parseToolList tests ---

func TestParseToolList_SimpleCommas(t *testing.T) {
	result := parseToolList([]string{"Bash,Read,Write"})
	if len(result) != 3 || result[0] != "Bash" || result[1] != "Read" || result[2] != "Write" {
		t.Errorf("expected [Bash Read Write], got %v", result)
	}
}

func TestParseToolList_PreservesParentheses(t *testing.T) {
	// "Bash(curl *),Read" should stay as two items, not split inside parens.
	result := parseToolList([]string{"Bash(curl", "*),Read"})
	if len(result) != 2 || result[0] != "Bash(curl *)" || result[1] != "Read" {
		t.Errorf("expected [Bash(curl *) Read], got %v", result)
	}
}

func TestParseToolList_CommaInsideParens(t *testing.T) {
	// SpawnInstance(worker,researcher) — comma inside parens should not split.
	result := parseToolList([]string{"SpawnInstance(worker,researcher),Read"})
	if len(result) != 2 || result[0] != "SpawnInstance(worker,researcher)" || result[1] != "Read" {
		t.Errorf("expected [SpawnInstance(worker,researcher) Read], got %v", result)
	}
}

func TestCommandToolsSet_RejectsMalformedRules(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/tools set researcher Bash(")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Invalid rule") {
		t.Errorf("expected invalid rule error, got: %s", result)
	}
	// Should not have set anything.
	if _, ok := cp.AgentTools("researcher"); ok {
		t.Error("malformed rule should not be stored")
	}
}

func TestCommandToolsDeny_RejectsMalformedRules(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/tools deny researcher Bash()")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Invalid rule") {
		t.Errorf("expected invalid rule error, got: %s", result)
	}
}

func TestParseToolList_SpaceSeparatedArgs(t *testing.T) {
	// When shell splits "Bash(curl *)" into ["Bash(curl", "*)"]
	result := parseToolList([]string{"Bash(curl", "*)"})
	if len(result) != 1 || result[0] != "Bash(curl *)" {
		t.Errorf("expected [Bash(curl *)], got %v", result)
	}
}

// --- Reset ---

func TestReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cp, _ := Load(path, testLogger())
	cp.SetSecret("API_KEY", "secret-value")
	if err := cp.Save(); err != nil {
		t.Fatal(err)
	}

	// File should exist after save.
	if _, err := os.Stat(path); err != nil {
		t.Fatal("expected config file to exist after Save")
	}

	if err := cp.Reset(); err != nil {
		t.Fatal(err)
	}

	// Secrets should be empty.
	if len(cp.SecretNames()) != 0 {
		t.Errorf("expected no secrets after Reset, got %v", cp.SecretNames())
	}

	// Config file should be removed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected config file to be removed after Reset")
	}
}

func TestReset_MissingFile(t *testing.T) {
	// Reset should succeed even when the config file doesn't exist.
	path := filepath.Join(t.TempDir(), "config.yaml")
	cp, _ := Load(path, testLogger())

	if err := cp.Reset(); err != nil {
		t.Fatalf("Reset should not error on missing file: %v", err)
	}
}

func TestReset_ClearsSigner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cp, _ := Load(path, testLogger())
	cp.SetPasswordHash("some-hash")

	// Force signer creation by calling TokenSigner.
	_, _ = cp.TokenSigner()

	if err := cp.Reset(); err != nil {
		t.Fatal(err)
	}

	// After reset, password hash should be gone.
	cp.mu.RLock()
	hash := cp.config.Auth.PasswordHash
	signer := cp.signer
	cp.mu.RUnlock()
	if hash != "" {
		t.Error("expected empty password hash after reset")
	}
	if signer != nil {
		t.Error("expected nil signer after reset")
	}
}

// --- Timezone ---

func TestTimezone_Default(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cp, _ := Load(path, testLogger())

	loc := cp.Timezone()
	if loc != time.UTC {
		t.Errorf("expected UTC, got %v", loc)
	}
}

func TestTimezone_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `timezone: "America/New_York"`
	os.WriteFile(path, []byte(data), 0o600)

	cp, _ := Load(path, testLogger())
	loc := cp.Timezone()
	if loc.String() != "America/New_York" {
		t.Errorf("expected America/New_York, got %v", loc)
	}
}

func TestTimezone_Invalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `timezone: "Not/A/Real/Zone"`
	os.WriteFile(path, []byte(data), 0o600)

	cp, _ := Load(path, testLogger())
	loc := cp.Timezone()
	if loc != time.UTC {
		t.Errorf("expected UTC fallback for invalid timezone, got %v", loc)
	}
}

// --- handleHelp ---

func TestCommandHelp(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/help")
	if err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"/help", "/clear", "/secrets", "/tools", "/cluster"} {
		if !strings.Contains(result, cmd) {
			t.Errorf("help text should mention %q", cmd)
		}
	}
}

// --- NodeApprovalCheck ---

func TestNodeApprovalCheck_Pending(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	status := cp.NodeApprovalCheck("unknown-node")
	if status != NodeStatusPending {
		t.Errorf("expected Pending, got %d", status)
	}
}

func TestNodeApprovalCheck_Approved(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.ApproveNode("node-1", "Worker 1")

	status := cp.NodeApprovalCheck("node-1")
	if status != NodeStatusApproved {
		t.Errorf("expected Approved, got %d", status)
	}
}

func TestNodeApprovalCheck_Revoked(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.ApproveNode("node-1", "Worker 1")
	cp.RevokeNode("node-1")

	status := cp.NodeApprovalCheck("node-1")
	if status != NodeStatusRevoked {
		t.Errorf("expected Revoked, got %d", status)
	}
}

func TestNodeApprovalCheck_RevokedTakesPrecedence(t *testing.T) {
	// If a node is in both maps (shouldn't happen normally), revoked wins.
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.ApproveNode("node-1", "Worker 1")

	// Manually add to revoked without removing from approved.
	cp.mu.Lock()
	if cp.config.Cluster.RevokedNodes == nil {
		cp.config.Cluster.RevokedNodes = make(map[string]RevokedNode)
	}
	cp.config.Cluster.RevokedNodes["node-1"] = RevokedNode{Name: "Worker 1"}
	cp.mu.Unlock()

	status := cp.NodeApprovalCheck("node-1")
	if status != NodeStatusRevoked {
		t.Errorf("expected Revoked to take precedence, got %d", status)
	}
}

// --- ClearAllClusterNodes ---

func TestClearAllClusterNodes(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.ApproveNode("node-1", "Worker 1")
	cp.ApproveNode("node-2", "Worker 2")
	cp.RevokeNode("node-1")

	cp.ClearAllClusterNodes()

	if nodes := cp.ApprovedNodes(); nodes != nil {
		t.Errorf("expected nil approved nodes, got %v", nodes)
	}
	if cp.IsNodeRevoked("node-1") {
		t.Error("expected no revoked nodes after clear")
	}
}

// --- Cluster setters ---

func TestSetClusterTrackerURL(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetClusterTrackerURL("https://tracker.example.com")
	if got := cp.ClusterTrackerURL(); got != "https://tracker.example.com" {
		t.Errorf("expected tracker URL, got %q", got)
	}
}

func TestSetClusterLeaderAddr(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetClusterLeaderAddr("10.0.0.1:9090")
	if got := cp.ClusterLeaderAddr(); got != "10.0.0.1:9090" {
		t.Errorf("expected leader addr, got %q", got)
	}
}

func TestSetClusterNodeName(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	cp.SetClusterNodeName("my-worker")
	if got := cp.ClusterNodeName(); got != "my-worker" {
		t.Errorf("expected node name, got %q", got)
	}
}

func TestClusterTrackerURL_ConfigVsEnvPrecedence(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetClusterTrackerURL("https://config-value.com")

	// Without env var, config value is used.
	if got := cp.ClusterTrackerURL(); got != "https://config-value.com" {
		t.Errorf("expected config value, got %q", got)
	}

	// Env var takes precedence.
	t.Setenv("HIRO_TRACKER_URL", "https://env-value.com")
	if got := cp.ClusterTrackerURL(); got != "https://env-value.com" {
		t.Errorf("expected env var to take precedence, got %q", got)
	}
}
