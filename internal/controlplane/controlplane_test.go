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

// --- Reload tests ---

func TestReload_ExternalEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n"), 0600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.SecretNames()) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(cp.SecretNames()))
	}

	// Simulate external edit: add a second secret.
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n  B: \"2\"\n"), 0600)
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
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n"), 0600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Write invalid YAML (tabs are illegal in YAML).
	os.WriteFile(path, []byte("\t\tinvalid"), 0600)
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
	os.WriteFile(path, []byte("secrets:\n  A: \"1\"\n"), 0600)

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
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$test\"\n  session_secret: \"0102030405060708091011121314151617181920212223242526272829303132\"\n"), 0600)

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
	os.WriteFile(path, data, 0600)

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
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$original\"\n  session_secret: \"abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234\"\n"), 0600)

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
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$changed\"\n  session_secret: \"abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234\"\n"), 0600)
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
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$same\"\n  session_secret: \"aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd\"\n"), 0600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	signer, err := cp.TokenSigner()
	if err != nil {
		t.Fatal(err)
	}

	// Rotate session_secret only (password hash unchanged).
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$same\"\n  session_secret: \"11223344112233441122334411223344112233441122334411223344aabbccdd\"\n"), 0600)
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

	// Delete clears default if matching.
	cp.SetDefaultProvider("anthropic")
	cp.DeleteProvider("anthropic")
	if cp.DefaultProvider() != "" {
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
	cp.SetDefaultProvider("openrouter")

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
		{"1234567890", "**********"},       // 10 chars: prefix+suffix would reveal all, so fully masked
		{"12345678901", "123456...8901"},    // 11 chars: just enough to mask 1 middle char
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

func TestCommandClusterTokenCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cp, _ := Load(path, testLogger())

	result, err := cp.HandleCommand("/cluster token create")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "default") || !strings.Contains(result, "created") {
		t.Errorf("unexpected result: %s", result)
	}

	tokens := cp.ClusterJoinTokens()
	if _, ok := tokens["default"]; !ok {
		t.Error("expected 'default' token to exist")
	}

	// Verify persisted to disk.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "join_tokens") {
		t.Error("expected join_tokens in config.yaml")
	}
}

func TestCommandClusterTokenCreateNamed(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/cluster token create worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "worker-1") {
		t.Errorf("unexpected result: %s", result)
	}

	tokens := cp.ClusterJoinTokens()
	if _, ok := tokens["worker-1"]; !ok {
		t.Error("expected 'worker-1' token to exist")
	}
}

func TestCommandClusterTokenRevoke(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetClusterJoinToken("test", "abc123")

	result, err := cp.HandleCommand("/cluster token revoke test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "revoked") {
		t.Errorf("unexpected result: %s", result)
	}

	tokens := cp.ClusterJoinTokens()
	if tokens != nil {
		if _, ok := tokens["test"]; ok {
			t.Error("expected token to be revoked")
		}
	}
}

func TestCommandClusterTokenList(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetClusterJoinToken("node-a", "abcdef1234567890abcdef1234567890")

	result, err := cp.HandleCommand("/cluster token list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "node-a") {
		t.Errorf("expected token name in output: %s", result)
	}
	// Token should be masked.
	if strings.Contains(result, "abcdef1234567890abcdef1234567890") {
		t.Error("full token should not appear in list output")
	}
	if !strings.Contains(result, "...") {
		t.Error("expected masked token with '...'")
	}
}

func TestCommandClusterTokenListEmpty(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	result, err := cp.HandleCommand("/cluster token list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No join tokens") {
		t.Errorf("expected empty message, got: %s", result)
	}
}

// --- Cluster config tests ---

func TestValidateJoinToken(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetClusterJoinToken("worker-1", "secret-token-1")
	cp.SetClusterJoinToken("worker-2", "secret-token-2")

	// Valid token returns name.
	if name := cp.ValidateJoinToken("secret-token-1"); name != "worker-1" {
		t.Errorf("expected 'worker-1', got %q", name)
	}
	if name := cp.ValidateJoinToken("secret-token-2"); name != "worker-2" {
		t.Errorf("expected 'worker-2', got %q", name)
	}

	// Invalid token returns empty.
	if name := cp.ValidateJoinToken("wrong-token"); name != "" {
		t.Errorf("expected empty string for invalid token, got %q", name)
	}
}

func TestClusterMode_Default(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if mode := cp.ClusterMode(); mode != "leader" {
		t.Errorf("expected 'leader' default, got %q", mode)
	}

	cp.SetClusterMode("worker")
	if mode := cp.ClusterMode(); mode != "worker" {
		t.Errorf("expected 'worker', got %q", mode)
	}
}

func TestClusterMode_EnvOverride(t *testing.T) {
	t.Setenv("HIVE_MODE", "worker")
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetClusterMode("leader")

	if mode := cp.ClusterMode(); mode != "worker" {
		t.Errorf("expected HIVE_MODE env var to take precedence, got %q", mode)
	}
}

func TestClusterTrackerURL_EnvOverride(t *testing.T) {
	t.Setenv("HIVE_TRACKER_URL", "https://env-tracker.example.com")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("cluster:\n  tracker_url: https://config-tracker.example.com\n"), 0600)

	cp, _ := Load(path, testLogger())
	if url := cp.ClusterTrackerURL(); url != "https://env-tracker.example.com" {
		t.Errorf("expected env var to take precedence, got %q", url)
	}
}

func TestClusterJoinTokens_Copy(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())
	cp.SetClusterJoinToken("a", "token-a")

	tokens := cp.ClusterJoinTokens()
	tokens["b"] = "token-b" // modify the copy

	// Original should be unaffected.
	tokens2 := cp.ClusterJoinTokens()
	if _, ok := tokens2["b"]; ok {
		t.Error("modifying returned map should not affect ControlPlane")
	}
}

func TestClusterJoinTokens_NilWhenEmpty(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	tokens := cp.ClusterJoinTokens()
	if tokens != nil {
		t.Errorf("expected nil for no tokens, got %v", tokens)
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
	os.WriteFile(filepath.Join(dir, "nowrite"), []byte("not a dir"), 0600)

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
	if cp.NeedsSetup() {
		t.Error("expected NeedsSetup=false after SetPasswordHash")
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
	os.WriteFile(path, []byte("auth:\n  password_hash: \"$2a$10$original\"\n  session_secret: \"aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd\"\n"), 0600)

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
  join_token: "tok123"
  node_name: "node-1"
  swarm_code: "swarm42"
`), 0600)

	cp, err := Load(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	if v := cp.ClusterLeaderAddr(); v != "leader:9090" {
		t.Errorf("ClusterLeaderAddr() = %q", v)
	}
	if v := cp.ClusterJoinToken(); v != "tok123" {
		t.Errorf("ClusterJoinToken() = %q", v)
	}
	if v := cp.ClusterNodeName(); v != "node-1" {
		t.Errorf("ClusterNodeName() = %q", v)
	}
	if v := cp.ClusterSwarmCode(); v != "swarm42" {
		t.Errorf("ClusterSwarmCode() = %q", v)
	}
}

func TestClusterSwarmCode_EnvOverride(t *testing.T) {
	t.Setenv("HIVE_SWARM_CODE", "env-swarm")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("cluster:\n  swarm_code: config-swarm\n"), 0600)

	cp, _ := Load(path, testLogger())
	if v := cp.ClusterSwarmCode(); v != "env-swarm" {
		t.Errorf("expected env var to win, got %q", v)
	}
}

// --- Provider getter tests ---

func TestDefaultModel(t *testing.T) {
	cp, _ := Load(filepath.Join(t.TempDir(), "config.yaml"), testLogger())

	if cp.DefaultModel() != "" {
		t.Error("expected empty default model")
	}
	cp.SetDefaultModel("claude-3-opus")
	if cp.DefaultModel() != "claude-3-opus" {
		t.Errorf("unexpected: %s", cp.DefaultModel())
	}
}

