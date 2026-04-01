package controlplane

import (
	"os"
	"time"
)

// ClusterMode returns the cluster mode: "standalone", "leader", or "worker".
// Returns empty string if not yet configured (pre-setup).
// HIRO_MODE env var takes precedence over config.yaml.
func (cp *ControlPlane) ClusterMode() string {
	if envMode := os.Getenv("HIRO_MODE"); envMode != "" {
		return envMode
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.Mode
}

// ClusterLeaderAddr returns the leader's gRPC address (worker mode).
func (cp *ControlPlane) ClusterLeaderAddr() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.LeaderAddr
}

// ClusterNodeName returns the human-friendly node name.
func (cp *ControlPlane) ClusterNodeName() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.NodeName
}

// ClusterTrackerURL returns the tracker URL for discovery.
// HIRO_TRACKER_URL env var takes precedence over config.yaml.
func (cp *ControlPlane) ClusterTrackerURL() string {
	if envURL := os.Getenv("HIRO_TRACKER_URL"); envURL != "" {
		return envURL
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.TrackerURL
}

// ClusterSwarmCode returns the swarm code for tracker discovery.
// HIRO_SWARM_CODE env var takes precedence over config.yaml.
func (cp *ControlPlane) ClusterSwarmCode() string {
	if v := os.Getenv("HIRO_SWARM_CODE"); v != "" {
		return v
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Cluster.SwarmCode
}

// IsNodeApproved checks if a node ID exists in the approved nodes map.
func (cp *ControlPlane) IsNodeApproved(nodeID string) bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if cp.config.Cluster.ApprovedNodes == nil {
		return false
	}
	_, ok := cp.config.Cluster.ApprovedNodes[nodeID]
	return ok
}

// ApproveNode adds a node to the approved list. Caller must call Save() to persist.
func (cp *ControlPlane) ApproveNode(nodeID, name string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.config.Cluster.ApprovedNodes == nil {
		cp.config.Cluster.ApprovedNodes = make(map[string]ApprovedNode)
	}
	cp.config.Cluster.ApprovedNodes[nodeID] = ApprovedNode{
		Name:       name,
		ApprovedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// RemoveApprovedNode removes a node from the approved list. Caller must call Save() to persist.
func (cp *ControlPlane) RemoveApprovedNode(nodeID string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.config.Cluster.ApprovedNodes, nodeID)
}

// ApprovedNodes returns a copy of the approved nodes map.
func (cp *ControlPlane) ApprovedNodes() map[string]ApprovedNode {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if cp.config.Cluster.ApprovedNodes == nil {
		return nil
	}
	nodes := make(map[string]ApprovedNode, len(cp.config.Cluster.ApprovedNodes))
	for k, v := range cp.config.Cluster.ApprovedNodes {
		nodes[k] = v
	}
	return nodes
}

// SetClusterMode sets the cluster mode.
func (cp *ControlPlane) SetClusterMode(mode string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Cluster.Mode = mode
}

// SetClusterTrackerURL sets the tracker URL for discovery.
func (cp *ControlPlane) SetClusterTrackerURL(url string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Cluster.TrackerURL = url
}

// SetClusterSwarmCode sets the swarm code for tracker discovery.
func (cp *ControlPlane) SetClusterSwarmCode(code string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Cluster.SwarmCode = code
}

// SetClusterLeaderAddr sets the leader's gRPC address (worker mode).
func (cp *ControlPlane) SetClusterLeaderAddr(addr string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Cluster.LeaderAddr = addr
}

// SetClusterNodeName sets the human-friendly node name.
func (cp *ControlPlane) SetClusterNodeName(name string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Cluster.NodeName = name
}
