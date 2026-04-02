package controlplane

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

// SetAgentTools sets the operator allow-tool override for a named agent.
// Preserves any existing deny rules.
func (cp *ControlPlane) SetAgentTools(name string, tools []string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	policy := cp.config.Agents[name]
	policy.Tools = tools
	cp.config.Agents[name] = policy
}

// ClearAgentTools removes the operator allow-tool override for a named agent.
// Preserves any existing deny rules. If no fields remain, the policy is removed.
func (cp *ControlPlane) ClearAgentTools(name string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	policy := cp.config.Agents[name]
	policy.Tools = nil
	if len(policy.DenyTools) == 0 {
		delete(cp.config.Agents, name)
	} else {
		cp.config.Agents[name] = policy
	}
}

// AgentDenyTools returns the operator-defined deny rules for the named agent.
func (cp *ControlPlane) AgentDenyTools(name string) []string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	policy, exists := cp.config.Agents[name]
	if !exists {
		return nil
	}
	return policy.DenyTools
}

// SetAgentDenyTools sets the operator deny-tool rules for a named agent.
// Preserves any existing allow rules.
func (cp *ControlPlane) SetAgentDenyTools(name string, denyTools []string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	policy := cp.config.Agents[name]
	policy.DenyTools = denyTools
	cp.config.Agents[name] = policy
}

// ClearAgentDenyTools removes the operator deny-tool rules for a named agent.
// Preserves any existing allow rules. If no fields remain, the policy is removed.
func (cp *ControlPlane) ClearAgentDenyTools(name string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	policy := cp.config.Agents[name]
	policy.DenyTools = nil
	if len(policy.Tools) == 0 {
		delete(cp.config.Agents, name)
	} else {
		cp.config.Agents[name] = policy
	}
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
