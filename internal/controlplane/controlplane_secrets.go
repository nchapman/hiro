package controlplane

import "sort"

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
