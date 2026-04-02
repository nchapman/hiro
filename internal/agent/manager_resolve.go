package agent

import (
	"fmt"

	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/models"
	"github.com/nchapman/hiro/internal/toolrules"
)

// spawnTool is injected into all agents.
var spawnTool = "SpawnInstance"

// coordinatorTools are injected only for coordinator-mode instances.
var coordinatorTools = []string{
	"CreatePersistentInstance", "ResumeInstance", "ListInstances", "SendMessage", "StopInstance", "DeleteInstance",
}

// persistentTools are injected for persistent and coordinator instances.
var persistentTools = []string{
	"TodoWrite", "HistorySearch", "HistoryRecall",
}

// computeEffectiveTools returns the set of tool names this instance can use,
// plus the parsed allow/deny rules for call-time enforcement.
//
// Tool name set is computed as the intersection of:
//  1. The agent's declared tools (from agent.md frontmatter)
//  2. The control plane override (if any)
//  3. The parent's effective tools (if any)
//
// Allow layers enforce per-source parameter restrictions at call time.
// A tool call must be allowed by ALL layers (within a layer, rules are OR'd;
// across layers, they are AND'd). Deny rules are merged from all sources;
// any matching deny rule blocks the call.
func (m *Manager) computeEffectiveTools(cfg config.AgentConfig, parentID string) (effective map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule, err error) {
	// Parse agent's declared tools as rules.
	var agentAllow []toolrules.Rule
	if cfg.DeclaredTools != nil {
		agentAllow, err = toolrules.ParseRules(cfg.DeclaredTools)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parsing agent tool rules: %w", err)
		}
	}

	// Parse agent's deny rules.
	if len(cfg.DenyTools) > 0 {
		agentDeny, parseErr := toolrules.ParseRules(cfg.DenyTools)
		if parseErr != nil {
			return nil, nil, nil, fmt.Errorf("parsing agent deny rules: %w", parseErr)
		}
		denyRules = append(denyRules, agentDeny...)
	}

	// Extract tool names from agent allow rules.
	effective = make(map[string]bool, len(agentAllow))
	for _, r := range agentAllow {
		effective[r.Tool] = true
	}

	// Add agent allow layer if it has parameterized rules.
	if hasParameterized(agentAllow) {
		allowLayers = append(allowLayers, agentAllow)
	}

	// Intersect with control plane override if present.
	if m.cp != nil {
		if cpToolStrs, ok := m.cp.AgentTools(cfg.Name); ok {
			cpAllow, parseErr := toolrules.ParseRules(cpToolStrs)
			if parseErr != nil {
				return nil, nil, nil, fmt.Errorf("parsing control plane tool rules: %w", parseErr)
			}
			cpNames := make(map[string]bool, len(cpAllow))
			for _, r := range cpAllow {
				cpNames[r.Tool] = true
			}
			for t := range effective {
				if !cpNames[t] {
					delete(effective, t)
				}
			}
			if hasParameterized(cpAllow) {
				allowLayers = append(allowLayers, filterRules(cpAllow, effective))
			}
		}

		// Control plane deny rules.
		if cpDenyStrs := m.cp.AgentDenyTools(cfg.Name); len(cpDenyStrs) > 0 {
			cpDeny, parseErr := toolrules.ParseRules(cpDenyStrs)
			if parseErr != nil {
				return nil, nil, nil, fmt.Errorf("parsing control plane deny rules: %w", parseErr)
			}
			denyRules = append(denyRules, cpDeny...)
		}
	}

	// Intersect with parent's effective tools and inherit its rules.
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
			allowLayers = append(allowLayers, parent.allowLayers...)
			denyRules = append(denyRules, parent.denyRules...)
		}
	}

	// Filter all allow layers to only include rules for effective tools,
	// and strip whole-tool grants that would silently nullify parameterized
	// restrictions in the same layer.
	for i, layer := range allowLayers {
		allowLayers[i] = stripRedundantWholeToolRules(filterRules(layer, effective))
	}

	// Remove tools that are wholly denied by any source.
	for _, r := range denyRules {
		if r.IsWholeTool() {
			delete(effective, r.Tool)
		}
	}

	// Filter deny rules to only include rules for tools in the effective set.
	denyRules = filterRules(denyRules, effective)

	return effective, allowLayers, denyRules, nil
}

// hasParameterized reports whether any rule has a non-empty pattern.
// Layers that are all whole-tool rules add no call-time restriction
// beyond the name-based check, so they can be omitted.
func hasParameterized(rules []toolrules.Rule) bool {
	for _, r := range rules {
		if !r.IsWholeTool() {
			return true
		}
	}
	return false
}

// stripRedundantWholeToolRules removes whole-tool rules for tools that
// also have parameterized rules in the same layer. Without this, a
// whole-tool grant like "Bash" silently nullifies "Bash(curl *)" in the
// same layer because the checker's OR semantics match the whole-tool
// rule first.
func stripRedundantWholeToolRules(rules []toolrules.Rule) []toolrules.Rule {
	// Find tools that have at least one parameterized rule.
	hasParam := make(map[string]bool)
	for _, r := range rules {
		if !r.IsWholeTool() {
			hasParam[r.Tool] = true
		}
	}

	// Remove whole-tool rules for those tools.
	var result []toolrules.Rule
	for _, r := range rules {
		if r.IsWholeTool() && hasParam[r.Tool] {
			continue // drop: parameterized rules take precedence
		}
		result = append(result, r)
	}
	return result
}

// filterRules returns only rules whose tool name is in the effective set.
func filterRules(rules []toolrules.Rule, effective map[string]bool) []toolrules.Rule {
	var filtered []toolrules.Rule
	for _, r := range rules {
		if effective[r.Tool] {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// buildAllowedToolsMap creates the AllowedTools map for agent.Options,
// adding mode-appropriate structural tools that bypass filtering.
func buildAllowedToolsMap(effective map[string]bool, mode config.AgentMode, hasSkills bool) map[string]bool {
	allowed := make(map[string]bool, len(effective)+10)
	for t := range effective {
		allowed[t] = true
	}

	// All instances get SpawnInstance; coordinators can use all modes.
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
		allowed["Skill"] = true
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
