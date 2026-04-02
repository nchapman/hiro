// Package controlplane manages operator-level configuration that agents
// cannot access or modify. It holds secrets, per-agent tool policies,
// authentication, and LLM provider settings. It reads from config.yaml
// at startup and writes state back on shutdown.
package controlplane

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/nchapman/hiro/internal/auth"
	"gopkg.in/yaml.v3"
)

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
	Tools     []string `yaml:"tools,omitempty"`
	DisallowedTools []string `yaml:"disallowed_tools,omitempty"`
}

// ApprovedNode represents a worker node that has been approved to join the cluster.
type ApprovedNode struct {
	Name       string `yaml:"name" json:"name"`
	ApprovedAt string `yaml:"approved_at" json:"approved_at"` // RFC3339 timestamp
}

// RevokedNode represents a worker node whose approval has been explicitly revoked.
type RevokedNode struct {
	Name      string `yaml:"name" json:"name"`
	RevokedAt string `yaml:"revoked_at" json:"revoked_at"` // RFC3339 timestamp
}

// ClusterConfig holds settings for leader-worker clustering.
type ClusterConfig struct {
	Mode          string                  `yaml:"mode,omitempty"`           // "standalone", "leader", or "worker"
	LeaderAddr    string                  `yaml:"leader_addr,omitempty"`    // gRPC address for worker→leader connection
	NodeName      string                  `yaml:"node_name,omitempty"`     // human-friendly node name
	TrackerURL    string                  `yaml:"tracker_url,omitempty"`   // discovery tracker URL (e.g. https://discover.hellohiro.ai)
	SwarmCode     string                  `yaml:"swarm_code,omitempty"`    // shared swarm code for tracker discovery
	ApprovedNodes map[string]ApprovedNode `yaml:"approved_nodes,omitempty"` // keyed by NodeID (hex sha256 of pubkey)
	RevokedNodes  map[string]RevokedNode `yaml:"revoked_nodes,omitempty"`  // keyed by NodeID — explicitly revoked
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

// initMaps ensures all map fields are non-nil. ApprovedNodes is intentionally
// excluded — it is lazily initialised in ApproveNode so that
// ApprovedNodes() returns nil when no nodes are approved.
func (cfg *Config) initMaps() {
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentPolicy)
	}
}

// ControlPlane holds operator-level state in memory during runtime.
// It is read from config.yaml at startup and written back on shutdown.
// All access is thread-safe.
type ControlPlane struct {
	mu             sync.RWMutex
	config         Config
	signer         *auth.TokenSigner // cached; invalidated on secret rotation
	path           string
	logger         *slog.Logger
	skipNextReload bool // set by Save to suppress the fsnotify-triggered Reload
}

// Load reads the control plane config from path. If the file does not
// exist, an empty config is returned (no error). This is the normal
// state on first run.
func Load(path string, logger *slog.Logger) (*ControlPlane, error) {
	cp := &ControlPlane{
		path:   path,
		logger: logger.With("component", "controlplane"),
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
	cfg.initMaps()
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

	// Suppress the fsnotify-triggered Reload that will fire from our own write.
	// Without this, a concurrent config mutation between Save and Reload would
	// be lost when Reload replaces in-memory state from disk.
	cp.skipNextReload = true

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
		cp.config.Cluster.Mode != "" ||
		cp.config.Cluster.TrackerURL != "" ||
		len(cp.config.Cluster.ApprovedNodes) > 0 ||
		len(cp.config.Cluster.RevokedNodes) > 0
}

// Reset wipes all in-memory state and removes the config file from disk.
// The node returns to a fresh first-run state (onboarding flow).
func (cp *ControlPlane) Reset() error {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	cp.config = Config{}
	cp.config.initMaps()
	cp.signer = nil

	if err := os.Remove(cp.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing config file: %w", err)
	}
	cp.logger.Info("control plane config reset")
	return nil
}

// Reload re-reads config.yaml from disk and replaces the in-memory state.
// If the file is missing or contains invalid YAML, the current state is
// preserved and a warning is logged (no error returned — the system keeps
// running with its current config). The cached TokenSigner is invalidated
// only if the password hash changed.
//
// Reloads triggered by our own Save are skipped to prevent a concurrent
// in-memory mutation from being clobbered by stale disk state.
func (cp *ControlPlane) Reload() error {
	cp.mu.Lock()
	if cp.skipNextReload {
		cp.skipNextReload = false
		cp.mu.Unlock()
		cp.logger.Debug("skipping self-triggered config reload")
		return nil
	}
	cp.mu.Unlock()

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
	cfg.initMaps()

	cp.mu.Lock()
	defer cp.mu.Unlock()

	// Invalidate signer if the password hash or session secret changed on disk.
	// This covers password rotation (external edit), manual secret rotation
	// (emergency session invalidation), and SetPasswordHash which clears both.
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
