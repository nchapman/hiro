package controlplane

import (
	"crypto/subtle"
	"os"
)

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

// ClusterTrackerURL returns the tracker URL for discovery.
// HIVE_TRACKER_URL env var takes precedence over config.yaml.
func (cp *ControlPlane) ClusterTrackerURL() string {
	if envURL := os.Getenv("HIVE_TRACKER_URL"); envURL != "" {
		return envURL
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.TrackerURL
}

// ClusterSwarmCode returns the swarm code for tracker discovery.
// HIVE_SWARM_CODE env var takes precedence over config.yaml.
func (cp *ControlPlane) ClusterSwarmCode() string {
	if v := os.Getenv("HIVE_SWARM_CODE"); v != "" {
		return v
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.SwarmCode
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
