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
