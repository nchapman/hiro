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

// applyInstanceToolConfig overrides the agent config's tool declarations with
// the instance's config.yaml values. Instance config is the source of truth
// for tool declarations — tools are seeded from agent.md at creation and owned
// by the instance thereafter. Falls back to agent.md if no instance tools exist
// (backward compat for pre-existing instances).
func applyInstanceToolConfig(instDir string, cfg *config.AgentConfig) {
	instCfg, err := config.LoadInstanceConfig(instDir)
	if err != nil || len(instCfg.AllowedTools) == 0 {
		return // no instance config or no tools declared — use agent.md defaults
	}
	cfg.AllowedTools = instCfg.AllowedTools
	cfg.DisallowedTools = instCfg.DisallowedTools
}

// computeEffectiveTools returns the set of tool names this instance can use,
// plus the parsed allow/deny rules for call-time enforcement.
//
// Tool name set is computed as the intersection of:
//  1. The instance's declared tools (from config.yaml, seeded from agent.md at creation)
//  2. The parent's effective tools (if any)
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

	// Intersect with parent rules (child can't have more tools than parent).
	allowLayers, denyRules = m.intersectParentRules(parentID, effective, allowLayers, denyRules)

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

// intersectParentRules intersects the effective tool set with the parent instance's
// effective tools and inherits its allow/deny rules. Modifies effective in place.
func (m *Manager) intersectParentRules(parentID string, effective map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule) ([][]toolrules.Rule, []toolrules.Rule) {
	if parentID == "" {
		return allowLayers, denyRules
	}
	m.mu.RLock()
	parent, ok := m.instances[parentID]
	m.mu.RUnlock()
	if !ok || parent.effectiveTools == nil {
		return allowLayers, denyRules
	}
	for t := range effective {
		if !parent.effectiveTools[t] {
			delete(effective, t)
		}
	}
	allowLayers = append(allowLayers, parent.allowLayers...)
	denyRules = append(denyRules, parent.denyRules...)
	return allowLayers, denyRules
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

// computeEffectiveEgress returns the effective network egress policy for an instance,
// computed as the intersection of the agent's declared egress and its parent's effective egress.
// Returns nil if the agent has no network access (no declaration or no overlap with parent).
func (m *Manager) computeEffectiveEgress(cfg config.AgentConfig, parentID string) []string {
	if cfg.NetworkEgress == nil {
		return nil // no network declared — default deny
	}

	if parentID == "" {
		return cfg.NetworkEgress // root agent — use as-is
	}

	m.mu.RLock()
	parent, ok := m.instances[parentID]
	m.mu.RUnlock()
	if !ok {
		return cfg.NetworkEgress // parent not found — use as-is (same as tool behavior)
	}
	if parent.effectiveEgress == nil {
		return nil // parent has no network — child can't either
	}

	return intersectEgress(cfg.NetworkEgress, parent.effectiveEgress)
}

// intersectEgress computes the intersection of child and parent egress policies.
// Wildcard ["*"] means unrestricted. Domain wildcards (*.github.com) are matched.
func intersectEgress(child, parent []string) []string {
	// Either side is wildcard → use the other (more restrictive) side.
	if len(parent) == 1 && parent[0] == "*" {
		return child
	}
	if len(child) == 1 && child[0] == "*" {
		return parent
	}

	// Both are specific lists — keep only child entries covered by parent.
	var result []string
	for _, c := range child {
		if egressCovers(parent, c) {
			result = append(result, c)
		}
	}
	return result
}

// egressCovers reports whether the allowlist covers the given domain pattern.
// A domain pattern is covered if any entry in the list matches it exactly or
// via wildcard (e.g., "*.github.com" in the list covers "api.github.com" in the query).
func egressCovers(list []string, domain string) bool {
	for _, entry := range list {
		if entry == domain {
			return true
		}
		if entryCoversWildcard(entry, domain) {
			return true
		}
	}
	return false
}

// entryCoversWildcard checks if a wildcard entry (e.g., "*.github.com") covers the domain.
func entryCoversWildcard(entry, domain string) bool {
	if len(entry) <= 2 || entry[:2] != "*." {
		return false
	}
	parentSuffix := entry[1:] // ".github.com"

	// Exact domain under parent wildcard (api.github.com under *.github.com).
	if len(domain) > len(parentSuffix) && domain[len(domain)-len(parentSuffix):] == parentSuffix {
		return true
	}
	// Child wildcard under parent wildcard (*.api.github.com under *.github.com).
	if len(domain) > 2 && domain[:2] == "*." {
		childSuffix := domain[1:] // ".api.github.com"
		if len(childSuffix) > len(parentSuffix) && childSuffix[len(childSuffix)-len(parentSuffix):] == parentSuffix {
			return true
		}
	}
	return false
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
	const injectedToolSlack = 10 // extra capacity for injected tools (spawn, persistent, skills, memory)
	allowed := make(map[string]bool, len(effective)+injectedToolSlack)
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
		applyModelOverride(&spec, agentModel)
	}

	// 3. Env override (highest priority).
	if m.opts.Model != "" {
		applyModelOverride(&spec, m.opts.Model)
	}

	return m.resolveProviderCredentials(spec)
}

// applyModelOverride merges a model string into a spec. A bare model name
// (no provider prefix) clears the inherited provider so the fallback search
// resolves the correct provider.
func applyModelOverride(spec *models.ModelSpec, model string) {
	parsed := models.ParseModelSpec(model)
	spec.Model = parsed.Model
	if parsed.Provider != "" {
		spec.Provider = parsed.Provider
	} else {
		spec.Provider = ""
	}
}

// resolveProviderCredentials resolves API key and base URL for a model spec
// using the control plane's configured providers.
func (m *Manager) resolveProviderCredentials(spec models.ModelSpec) (models.ModelSpec, string, string, error) {
	if spec.IsEmpty() {
		// No model specified anywhere — fall back to default provider credentials.
		pt, apiKey, baseURL, ok := m.cp.ProviderInfo()
		if !ok {
			return spec, "", "", nil
		}
		spec.Provider = pt
		return spec, apiKey, baseURL, nil
	}

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
