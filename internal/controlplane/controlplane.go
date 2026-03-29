// Package controlplane manages operator-level configuration that agents
// cannot access or modify. It holds secrets, per-agent tool policies,
// authentication, and LLM provider settings. It reads from config.yaml
// at startup and writes state back on shutdown.
package controlplane

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/hivebot/internal/auth"
	"gopkg.in/yaml.v3"
)

const sessionTTL = 24 * time.Hour

// AuthConfig holds authentication settings.
type AuthConfig struct {
	PasswordHash  string `yaml:"password_hash,omitempty"`
	SessionSecret string `yaml:"session_secret,omitempty"` // hex-encoded HMAC signing key
}

// ProviderConfig defines an LLM provider. The map key is the provider
// type (e.g. "anthropic", "openrouter"), so only one entry per type.
type ProviderConfig struct {
	APIKey  string `yaml:"api_key" json:"api_key"`                      // provider API key
	BaseURL string `yaml:"base_url,omitempty" json:"base_url,omitempty"` // optional API base URL override
}

// AgentPolicy defines operator-level overrides for a named agent.
type AgentPolicy struct {
	Tools []string `yaml:"tools,omitempty"`
}

// ClusterConfig holds settings for leader-worker clustering.
type ClusterConfig struct {
	Mode       string            `yaml:"mode,omitempty"`        // "leader" (default) or "worker"
	LeaderAddr string            `yaml:"leader_addr,omitempty"` // gRPC address for worker→leader connection
	JoinToken  string            `yaml:"join_token,omitempty"`  // auth token (worker mode)
	NodeName   string            `yaml:"node_name,omitempty"`   // human-friendly node name
	JoinTokens map[string]string `yaml:"join_tokens,omitempty"` // named tokens for node auth (leader mode)
}

// Config is the on-disk representation of the control plane state.
type Config struct {
	Auth            AuthConfig                `yaml:"auth,omitempty"`
	Providers       map[string]ProviderConfig `yaml:"providers,omitempty"`       // keyed by provider type
	DefaultProvider string                    `yaml:"default_provider,omitempty"` // provider type to use by default
	DefaultModel    string                    `yaml:"default_model,omitempty"`
	Secrets         map[string]string         `yaml:"secrets,omitempty"`
	Agents          map[string]AgentPolicy    `yaml:"agents,omitempty"`
	Cluster         ClusterConfig             `yaml:"cluster,omitempty"`
}

// ControlPlane holds operator-level state in memory during runtime.
// It is read from config.yaml at startup and written back on shutdown.
// All access is thread-safe.
type ControlPlane struct {
	mu     sync.RWMutex
	config Config
	signer *auth.TokenSigner // cached; invalidated on secret rotation
	path   string
	logger *slog.Logger
}

// Load reads the control plane config from path. If the file does not
// exist, an empty config is returned (no error). This is the normal
// state on first run.
func Load(path string, logger *slog.Logger) (*ControlPlane, error) {
	cp := &ControlPlane{
		path:   path,
		logger: logger,
		config: Config{
			Providers: make(map[string]ProviderConfig),
			Secrets:   make(map[string]string),
			Agents:    make(map[string]AgentPolicy),
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("no config.yaml found, starting with empty control plane", "path", path)
			return cp, nil
		}
		return nil, fmt.Errorf("reading control plane config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing control plane config: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentPolicy)
	}
	cp.config = cfg
	logger.Info("loaded control plane config", "path", path,
		"providers", len(cfg.Providers), "secrets", len(cfg.Secrets),
		"agent_policies", len(cfg.Agents))
	return cp, nil
}

// Save writes the current in-memory state back to the config file.
// Uses a write lock to prevent concurrent disk writes from racing.
func (cp *ControlPlane) Save() error {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.saveUnlocked()
}

// saveUnlocked writes config to disk. Caller must hold mu for writing.
func (cp *ControlPlane) saveUnlocked() error {
	// Don't write an empty file if there's nothing to persist.
	if !cp.hasContent() {
		return nil
	}

	data, err := yaml.Marshal(&cp.config)
	if err != nil {
		return fmt.Errorf("marshaling control plane config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cp.path), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Write with restrictive permissions since this contains secrets.
	if err := os.WriteFile(cp.path, data, 0600); err != nil {
		return fmt.Errorf("writing control plane config: %w", err)
	}

	cp.logger.Info("saved control plane config", "path", cp.path)
	return nil
}

// hasContent returns true if there is any state worth persisting.
// Must be called with mu held.
func (cp *ControlPlane) hasContent() bool {
	return cp.config.Auth.PasswordHash != "" ||
		cp.config.Auth.SessionSecret != "" ||
		len(cp.config.Providers) > 0 ||
		cp.config.DefaultProvider != "" ||
		cp.config.DefaultModel != "" ||
		len(cp.config.Secrets) > 0 ||
		len(cp.config.Agents) > 0 ||
		cp.config.Cluster.Mode != ""
}

// Reload re-reads config.yaml from disk and replaces the in-memory state.
// If the file is missing or contains invalid YAML, the current state is
// preserved and a warning is logged (no error returned — the system keeps
// running with its current config). The cached TokenSigner is invalidated
// only if the password hash changed.
func (cp *ControlPlane) Reload() error {
	data, err := os.ReadFile(cp.path)
	if err != nil {
		if os.IsNotExist(err) {
			cp.logger.Warn("config.yaml not found during reload, keeping current state", "path", cp.path)
			return nil
		}
		return fmt.Errorf("reading config.yaml for reload: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		cp.logger.Warn("invalid YAML in config.yaml, keeping current state", "path", cp.path, "error", err)
		return nil
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentPolicy)
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()

	// Invalidate signer if either the password hash or session secret changed.
	// This covers both password rotation (which changes the hash) and manual
	// session secret rotation (e.g., emergency session invalidation).
	if cfg.Auth.PasswordHash != cp.config.Auth.PasswordHash ||
		cfg.Auth.SessionSecret != cp.config.Auth.SessionSecret {
		cp.signer = nil
	}

	cp.config = cfg
	cp.logger.Info("reloaded config.yaml from disk", "path", cp.path,
		"providers", len(cfg.Providers), "secrets", len(cfg.Secrets),
		"agent_policies", len(cfg.Agents))
	return nil
}

// Path returns the absolute path to the config file.
func (cp *ControlPlane) Path() string {
	return cp.path
}

// --- Authentication ---

// NeedsSetup returns true if no admin password has been set (first run).
func (cp *ControlPlane) NeedsSetup() bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Auth.PasswordHash == ""
}

// PasswordHash returns the bcrypt password hash.
func (cp *ControlPlane) PasswordHash() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Auth.PasswordHash
}

// SetPasswordHash stores the bcrypt password hash and rotates the session
// secret, invalidating all existing sessions.
func (cp *ControlPlane) SetPasswordHash(hash string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Auth.PasswordHash = hash
	// Rotate session secret to invalidate existing sessions.
	cp.config.Auth.SessionSecret = ""
	cp.signer = nil
}

// TokenSigner returns a cached HMAC token signer for session authentication.
// On first call, it generates a signing secret and persists it to config.yaml
// so sessions survive restarts. The signer is invalidated when the password
// changes (which rotates the secret).
func (cp *ControlPlane) TokenSigner() (*auth.TokenSigner, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	if cp.signer != nil {
		return cp.signer, nil
	}

	var secret []byte
	if cp.config.Auth.SessionSecret != "" {
		var err error
		secret, err = hex.DecodeString(cp.config.Auth.SessionSecret)
		if err != nil {
			return nil, fmt.Errorf("decoding session secret: %w", err)
		}
	} else {
		var err error
		secret, err = auth.GenerateSecret()
		if err != nil {
			return nil, fmt.Errorf("generating session secret: %w", err)
		}
		cp.config.Auth.SessionSecret = hex.EncodeToString(secret)

		// Persist immediately so the secret survives crashes.
		if err := cp.saveUnlocked(); err != nil {
			cp.config.Auth.SessionSecret = "" // roll back
			return nil, fmt.Errorf("persisting session secret: %w", err)
		}
	}

	cp.signer = auth.NewTokenSigner(secret, sessionTTL)
	return cp.signer, nil
}

// --- Providers ---

// IsConfigured returns true if at least one provider with an API key exists.
func (cp *ControlPlane) IsConfigured() bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	for _, p := range cp.config.Providers {
		if p.APIKey != "" {
			return true
		}
	}
	return false
}

// ProviderInfo returns the default provider's type, API key, and base URL.
// This is the interface the Manager uses to resolve provider config.
// Uses DefaultProvider if set, otherwise falls back to the alphabetically
// first configured provider.
func (cp *ControlPlane) ProviderInfo() (providerType string, apiKey string, baseURL string, ok bool) {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	// Use explicit default if set and configured.
	if dp := cp.config.DefaultProvider; dp != "" {
		if p, exists := cp.config.Providers[dp]; exists && p.APIKey != "" {
			return dp, p.APIKey, p.BaseURL, true
		}
	}

	// Fall back to the alphabetically first provider with an API key.
	names := make([]string, 0, len(cp.config.Providers))
	for name := range cp.config.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cp.config.Providers[name]
		if p.APIKey != "" {
			return name, p.APIKey, p.BaseURL, true
		}
	}
	return "", "", "", false
}

// DefaultProvider returns the default provider type.
func (cp *ControlPlane) DefaultProvider() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.DefaultProvider
}

// SetDefaultProvider sets the default provider type.
func (cp *ControlPlane) SetDefaultProvider(providerType string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.DefaultProvider = providerType
}

// DefaultModel returns the global default model override.
func (cp *ControlPlane) DefaultModel() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.DefaultModel
}

// SetDefaultModel sets the global default model override.
func (cp *ControlPlane) SetDefaultModel(model string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.DefaultModel = model
}

// ProviderByType returns the API key and base URL for a specific provider type.
// Used by the Manager when an agent overrides the default provider.
func (cp *ControlPlane) ProviderByType(providerType string) (apiKey string, baseURL string, ok bool) {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	p, exists := cp.config.Providers[providerType]
	if !exists || p.APIKey == "" {
		return "", "", false
	}
	return p.APIKey, p.BaseURL, true
}

// ConfiguredProviderTypes returns a sorted list of all configured provider type keys.
func (cp *ControlPlane) ConfiguredProviderTypes() []string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	types := make([]string, 0, len(cp.config.Providers))
	for k, v := range cp.config.Providers {
		if v.APIKey != "" {
			types = append(types, k)
		}
	}
	sort.Strings(types)
	return types
}

// GetProvider returns a provider by type name.
func (cp *ControlPlane) GetProvider(providerType string) (ProviderConfig, bool) {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	p, ok := cp.config.Providers[providerType]
	return p, ok
}

// SetProvider creates or updates a provider keyed by type.
func (cp *ControlPlane) SetProvider(providerType string, cfg ProviderConfig) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Providers[providerType] = cfg
}

// DeleteProvider removes a provider by type.
func (cp *ControlPlane) DeleteProvider(providerType string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.config.Providers, providerType)
	// Clear default if we just deleted it.
	if cp.config.DefaultProvider == providerType {
		cp.config.DefaultProvider = ""
	}
}

// ListProviders returns a copy of all providers with API keys masked
// (only last 4 characters visible). Suitable for API responses.
func (cp *ControlPlane) ListProviders() map[string]ProviderConfig {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	result := make(map[string]ProviderConfig, len(cp.config.Providers))
	for k, v := range cp.config.Providers {
		v.APIKey = maskKey(v.APIKey)
		result[k] = v
	}
	return result
}

// maskKey returns a masked version of an API key showing a short prefix
// and the last 4 characters (e.g. "sk-or-...4xBq").
func maskKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	// Show up to 6 chars of prefix + ... + last 4 chars.
	prefix := key[:6]
	suffix := key[len(key)-4:]
	return prefix + "..." + suffix
}

// --- Secrets ---

// SecretNames returns a sorted list of secret names (never values).
// Used to populate the agent's system prompt.
func (cp *ControlPlane) SecretNames() []string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	names := make([]string, 0, len(cp.config.Secrets))
	for name := range cp.config.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SecretEnv returns secrets formatted as environment variable assignments
// ("KEY=VALUE") for injection into bash commands. Called at each bash
// invocation to pick up the latest secrets.
func (cp *ControlPlane) SecretEnv() []string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	env := make([]string, 0, len(cp.config.Secrets))
	for name, value := range cp.config.Secrets {
		env = append(env, name+"="+value)
	}
	return env
}

// SetSecret stores or updates a secret.
func (cp *ControlPlane) SetSecret(name, value string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Secrets[name] = value
}

// DeleteSecret removes a secret. No-op if it doesn't exist.
func (cp *ControlPlane) DeleteSecret(name string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.config.Secrets, name)
}

// --- Agent Tool Policies ---

// AgentTools returns the operator-defined tool override for the named
// agent and whether an override exists. If ok is false, the agent has
// no control plane restriction (use its declared tools from agent.md).
func (cp *ControlPlane) AgentTools(name string) (tools []string, ok bool) {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	policy, exists := cp.config.Agents[name]
	if !exists {
		return nil, false
	}
	return policy.Tools, true
}

// SetAgentTools sets the operator tool override for a named agent.
func (cp *ControlPlane) SetAgentTools(name string, tools []string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Agents[name] = AgentPolicy{Tools: tools}
}

// ClearAgentTools removes the operator tool override for a named agent,
// reverting it to its declared tools from agent.md.
func (cp *ControlPlane) ClearAgentTools(name string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.config.Agents, name)
}

// AllPolicies returns a copy of all agent policies for display.
func (cp *ControlPlane) AllPolicies() map[string]AgentPolicy {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	result := make(map[string]AgentPolicy, len(cp.config.Agents))
	for k, v := range cp.config.Agents {
		result[k] = v
	}
	return result
}

// --- Cluster ---

// ClusterMode returns the cluster mode: "leader" (default) or "worker".
// HIVE_MODE env var takes precedence over config.yaml.
func (cp *ControlPlane) ClusterMode() string {
	if envMode := os.Getenv("HIVE_MODE"); envMode != "" {
		return envMode
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if cp.config.Cluster.Mode == "" {
		return "leader"
	}
	return cp.config.Cluster.Mode
}

// ClusterLeaderAddr returns the leader's gRPC address (worker mode).
func (cp *ControlPlane) ClusterLeaderAddr() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.LeaderAddr
}

// ClusterJoinToken returns the join token (worker mode).
func (cp *ControlPlane) ClusterJoinToken() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.JoinToken
}

// ClusterNodeName returns the human-friendly node name.
func (cp *ControlPlane) ClusterNodeName() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.NodeName
}

// ClusterJoinTokens returns a copy of the named join tokens (leader mode).
func (cp *ControlPlane) ClusterJoinTokens() map[string]string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if cp.config.Cluster.JoinTokens == nil {
		return nil
	}
	tokens := make(map[string]string, len(cp.config.Cluster.JoinTokens))
	for k, v := range cp.config.Cluster.JoinTokens {
		tokens[k] = v
	}
	return tokens
}

// ValidateJoinToken checks if a token matches any named join token.
// Returns the token name if valid, empty string if not.
// Uses constant-time comparison to prevent timing side-channel attacks.
func (cp *ControlPlane) ValidateJoinToken(token string) string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	// Compare all tokens to prevent leaking token count via timing.
	found := ""
	for name, t := range cp.config.Cluster.JoinTokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			found = name
		}
	}
	return found
}

// SetClusterMode sets the cluster mode.
func (cp *ControlPlane) SetClusterMode(mode string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Cluster.Mode = mode
}

// SetClusterJoinToken adds or updates a named join token.
func (cp *ControlPlane) SetClusterJoinToken(name, token string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.config.Cluster.JoinTokens == nil {
		cp.config.Cluster.JoinTokens = make(map[string]string)
	}
	cp.config.Cluster.JoinTokens[name] = token
}

// DeleteClusterJoinToken removes a named join token.
func (cp *ControlPlane) DeleteClusterJoinToken(name string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.config.Cluster.JoinTokens, name)
}
