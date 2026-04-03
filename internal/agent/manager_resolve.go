package agent

import (
	"fmt"

	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/models"
	"github.com/nchapman/hiro/internal/toolrules"
)

// spawnTool is injected into all agents.
var spawnTool = "SpawnInstance"

// persistentTools are injected for persistent instances.
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
	if cfg.AllowedTools != nil {
		agentAllow, err = toolrules.ParseRules(cfg.AllowedTools)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parsing agent tool rules: %w", err)
		}
	}

	// Parse agent's deny rules.
	if len(cfg.DisallowedTools) > 0 {
		agentDeny, parseErr := toolrules.ParseRules(cfg.DisallowedTools)
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
		if cpDenyStrs := m.cp.AgentDisallowedTools(cfg.Name); len(cpDenyStrs) > 0 {
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
// adding mode-appropriate structural tools that bypass allowed_tools filtering.
//
// Structural tools (injected unconditionally, cannot be denied via allowed_tools):
//   - SpawnInstance: all instances
//   - AddMemory, ForgetMemory, TodoWrite, HistorySearch, HistoryRecall: persistent instances
//   - Skill: instances with skills available
//
// These are fundamental to the agent runtime and cannot be opted out of.
// Control-plane deny rules can still block them at call time.
//
// SECURITY: Management tools (CreatePersistentInstance, ResumeInstance,
// StopInstance, DeleteInstance, SendMessage, ListInstances, ListNodes)
// must NOT be added here unconditionally. They are only available to
// agents that explicitly declare them in allowed_tools.
func buildAllowedToolsMap(effective map[string]bool, mode config.AgentMode, hasSkills bool) map[string]bool {
	allowed := make(map[string]bool, len(effective)+10)
	for t := range effective {
		allowed[t] = true
	}

	// Structural tools — always injected regardless of allowed_tools.
	allowed[spawnTool] = true

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

// resolveModelString is a convenience wrapper around resolveModelSpec
// that returns just the "provider/model" string. Used in paths that
// only need the display value (e.g. restore).
func (m *Manager) resolveModelString(agentModel string) string {
	spec, _, _, _ := m.resolveModelSpec(agentModel)
	return spec.String()
}

// --- Config resolution and push ---

// resolveModelSpec resolves the model spec using priority:
// CP default < agent definition < env override (HIRO_MODEL).
// Then resolves provider credentials from the control plane.
//
// Returns the resolved spec, API key, and base URL. If no CP is
// configured, returns empty values with no error (test mode).
func (m *Manager) resolveModelSpec(agentModel string) (spec models.ModelSpec, apiKey, baseURL string, err error) {
	if m.cp == nil {
		// No control plane — test mode. Parse agent model if provided.
		if agentModel != "" {
			spec = models.ParseModelSpec(agentModel)
		}
		return spec, "", "", nil
	}

	// 1. CP default.
	spec = m.cp.DefaultModelSpec()

	// 2. Agent definition override.
	if agentModel != "" {
		agentSpec := models.ParseModelSpec(agentModel)
		spec.Model = agentSpec.Model
		if agentSpec.Provider != "" {
			spec.Provider = agentSpec.Provider
		} else {
			// Bare model name — clear inherited provider so the
			// fallback search resolves the correct provider.
			spec.Provider = ""
		}
	}

	// 3. Env override (highest priority).
	if m.opts.Model != "" {
		envSpec := models.ParseModelSpec(m.opts.Model)
		spec.Model = envSpec.Model
		if envSpec.Provider != "" {
			spec.Provider = envSpec.Provider
		} else {
			spec.Provider = ""
		}
	}

	if spec.IsEmpty() {
		// No model specified anywhere — fall back to default provider
		// credentials. The provider SDK will use its own default model.
		pt, apiKey, baseURL, ok := m.cp.ProviderInfo()
		if !ok {
			return spec, "", "", nil
		}
		spec.Provider = pt
		return spec, apiKey, baseURL, nil
	}

	// Resolve provider credentials.
	if spec.Provider != "" {
		apiKey, baseURL, ok := m.cp.ProviderByType(spec.Provider)
		if !ok {
			return spec, "", "", fmt.Errorf("provider %q not configured", spec.Provider)
		}
		return spec, apiKey, baseURL, nil
	}

	// Bare model name — search configured providers for a match.
	for _, pt := range m.cp.ConfiguredProviderTypes() {
		for _, mi := range models.ModelsForProvider(pt) {
			if mi.ID == spec.Model {
				apiKey, baseURL, ok := m.cp.ProviderByType(pt)
				if ok {
					spec.Provider = pt
					return spec, apiKey, baseURL, nil
				}
			}
		}
	}

	// No match found — fall back to default provider credentials.
	pt, apiKey, baseURL, ok := m.cp.ProviderInfo()
	if !ok {
		return spec, "", "", fmt.Errorf("no LLM provider configured")
	}
	spec.Provider = pt
	return spec, apiKey, baseURL, nil
}
