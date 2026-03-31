package agent

import (
	"fmt"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/models"
)

// spawnTool is injected into all agents.
var spawnTool = "spawn_instance"

// coordinatorTools are injected only for coordinator-mode instances.
var coordinatorTools = []string{
	"resume_instance", "list_instances", "send_message", "stop_instance", "delete_instance",
}

// persistentTools are injected for persistent and coordinator instances.
var persistentTools = []string{
	"todos", "history_search", "history_recall",
}

// computeEffectiveTools returns the set of built-in tools this instance is
// allowed to use, computed as the intersection of:
//  1. The agent's declared tools (from agent.md frontmatter)
//  2. The control plane override (if any)
//  3. The parent's effective tools (if any)
//
// Returns nil if the agent has no restrictions (all tools allowed).
func (m *Manager) computeEffectiveTools(cfg config.AgentConfig, parentID string) map[string]bool {
	// Start with declared tools from agent.md.
	var effective map[string]bool
	if cfg.DeclaredTools != nil {
		effective = make(map[string]bool, len(cfg.DeclaredTools))
		for _, t := range cfg.DeclaredTools {
			effective[t] = true
		}
	}
	// No declared tools = closed by default (empty set, not nil).
	if effective == nil {
		effective = make(map[string]bool)
	}

	// Intersect with control plane override if present.
	if m.cp != nil {
		if cpTools, ok := m.cp.AgentTools(cfg.Name); ok {
			cpSet := make(map[string]bool, len(cpTools))
			for _, t := range cpTools {
				cpSet[t] = true
			}
			for t := range effective {
				if !cpSet[t] {
					delete(effective, t)
				}
			}
		}
	}

	// Intersect with parent's effective tools if parent exists.
	if parentID != "" {
		m.mu.RLock()
		parent, ok := m.instances[parentID]
		m.mu.RUnlock()
		if ok && parent.effectiveTools != nil {
			for t := range effective {
				if !parent.effectiveTools[t] {
					delete(effective, t)
				}
			}
		}
	}

	return effective
}

// buildAllowedToolsMap creates the AllowedTools map for agent.Options,
// adding mode-appropriate structural tools that bypass filtering.
func buildAllowedToolsMap(effective map[string]bool, mode config.AgentMode, hasSkills bool) map[string]bool {
	allowed := make(map[string]bool, len(effective)+10)
	for t := range effective {
		allowed[t] = true
	}

	// All instances get spawn_instance; coordinators can use all modes.
	allowed[spawnTool] = true

	// Coordinator instances get full instance management tools.
	if mode == config.ModeCoordinator {
		for _, t := range coordinatorTools {
			allowed[t] = true
		}
	}

	// Persistent and coordinator instances get memory/todos/history tools.
	if mode.IsPersistent() {
		for _, t := range persistentTools {
			allowed[t] = true
		}
	}

	if hasSkills {
		allowed["use_skill"] = true
	}
	return allowed
}

// --- Config resolution and push ---

// resolveProvider returns the default provider type, API key, and base URL.
func (m *Manager) resolveProvider() (provider, apiKey, baseURL string, err error) {
	if m.cp == nil {
		return "", "", "", nil
	}
	provider, apiKey, baseURL, ok := m.cp.ProviderInfo()
	if !ok {
		return "", "", "", fmt.Errorf("no LLM provider configured")
	}
	return provider, apiKey, baseURL, nil
}

// resolveProviderForModel finds which configured provider offers the given model.
func (m *Manager) resolveProviderForModel(model string) (provider, apiKey, baseURL string, err error) {
	if m.cp == nil {
		return "", "", "", fmt.Errorf("no control plane configured")
	}
	for _, pt := range m.cp.ConfiguredProviderTypes() {
		for _, mi := range models.ModelsForProvider(pt) {
			if mi.ID == model {
				key, bu, ok := m.cp.ProviderByType(pt)
				if ok {
					return pt, key, bu, nil
				}
			}
		}
	}
	return "", "", "", fmt.Errorf("model %q not found in any configured provider", model)
}

// resolveModel returns the resolved model from the control plane default
// or the environment variable override.
func (m *Manager) resolveModel() string {
	var model string
	if m.cp != nil {
		model = m.cp.DefaultModel()
	}
	if m.opts.Model != "" {
		model = m.opts.Model
	}
	return model
}
