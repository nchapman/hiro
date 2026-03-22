// Package controlplane manages operator-level configuration that agents
// cannot access or modify. It holds secrets and per-agent tool policies,
// reads from config.yaml at startup, and writes state back on shutdown.
package controlplane

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk representation of the control plane state.
type Config struct {
	Secrets map[string]string      `yaml:"secrets,omitempty"`
	Agents  map[string]AgentPolicy `yaml:"agents,omitempty"`
}

// AgentPolicy defines operator-level overrides for a named agent.
type AgentPolicy struct {
	Tools []string `yaml:"tools,omitempty"`
}

// ControlPlane holds operator-level state in memory during runtime.
// It is read from config.yaml at startup and written back on shutdown.
// All access is thread-safe.
type ControlPlane struct {
	mu     sync.RWMutex
	config Config
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
			Secrets: make(map[string]string),
			Agents:  make(map[string]AgentPolicy),
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
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentPolicy)
	}
	cp.config = cfg
	logger.Info("loaded control plane config", "path", path,
		"secrets", len(cfg.Secrets), "agent_policies", len(cfg.Agents))
	return cp, nil
}

// Save writes the current in-memory state back to the config file.
// Called during graceful shutdown.
func (cp *ControlPlane) Save() error {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	// Don't write an empty file if there's nothing to persist.
	if len(cp.config.Secrets) == 0 && len(cp.config.Agents) == 0 {
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

// Path returns the absolute path to the config file.
func (cp *ControlPlane) Path() string {
	return cp.path
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
