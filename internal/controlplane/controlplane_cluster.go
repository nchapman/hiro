package controlplane

import (
	"maps"
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

// ClusterAdvertiseAddresses returns the configured advertise addresses for
// this node. Returns a copy so callers can mutate freely. Empty slice is
// normal — discovery falls back to the tracker's observed source IP.
func (cp *ControlPlane) ClusterAdvertiseAddresses() []string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if len(cp.config.Cluster.AdvertiseAddresses) == 0 {
		return nil
	}
	out := make([]string, len(cp.config.Cluster.AdvertiseAddresses))
	copy(out, cp.config.Cluster.AdvertiseAddresses)
	return out
}

// NodeApprovalStatus represents the approval state of a cluster node.
type NodeApprovalStatus int

const (
	// NodeStatusPending means the node is neither approved nor revoked.
	NodeStatusPending NodeApprovalStatus = iota
	// NodeStatusApproved means the node is in the approved list.
	NodeStatusApproved
	// NodeStatusRevoked means the node has been explicitly revoked.
	NodeStatusRevoked
)

// NodeApprovalCheck returns the approval status of a node atomically,
// reading both approved and revoked maps under a single lock. This
// eliminates the TOCTOU race of checking them separately.
func (cp *ControlPlane) NodeApprovalCheck(nodeID string) NodeApprovalStatus {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if cp.config.Cluster.RevokedNodes != nil {
		if _, ok := cp.config.Cluster.RevokedNodes[nodeID]; ok {
			return NodeStatusRevoked
		}
	}
	if cp.config.Cluster.ApprovedNodes != nil {
		if _, ok := cp.config.Cluster.ApprovedNodes[nodeID]; ok {
			return NodeStatusApproved
		}
	}
	return NodeStatusPending
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

// RevokeNode removes a node from the approved list and adds it to the revoked
// list so the leader can reject future connection attempts. Caller must call Save().
func (cp *ControlPlane) RevokeNode(nodeID string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	// Capture the name before deleting from approved.
	name := ""
	if n, ok := cp.config.Cluster.ApprovedNodes[nodeID]; ok {
		name = n.Name
	}
	delete(cp.config.Cluster.ApprovedNodes, nodeID)
	if cp.config.Cluster.RevokedNodes == nil {
		cp.config.Cluster.RevokedNodes = make(map[string]RevokedNode)
	}
	cp.config.Cluster.RevokedNodes[nodeID] = RevokedNode{
		Name:      name,
		RevokedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// IsNodeRevoked checks if a node has been explicitly revoked.
func (cp *ControlPlane) IsNodeRevoked(nodeID string) bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if cp.config.Cluster.RevokedNodes == nil {
		return false
	}
	_, ok := cp.config.Cluster.RevokedNodes[nodeID]
	return ok
}

// ClearRevokedNode removes a node from the revoked list, allowing it to
// appear as pending again on next connection. Caller must call Save().
func (cp *ControlPlane) ClearRevokedNode(nodeID string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.config.Cluster.RevokedNodes, nodeID)
}

// ApprovedNodes returns a copy of the approved nodes map.
func (cp *ControlPlane) ApprovedNodes() map[string]ApprovedNode {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	if cp.config.Cluster.ApprovedNodes == nil {
		return nil
	}
	nodes := make(map[string]ApprovedNode, len(cp.config.Cluster.ApprovedNodes))
	maps.Copy(nodes, cp.config.Cluster.ApprovedNodes)
	return nodes
}

// ClearAllClusterNodes removes all approved and revoked nodes.
// Used during cluster reset to prevent stale node state from surviving mode changes.
func (cp *ControlPlane) ClearAllClusterNodes() {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Cluster.ApprovedNodes = nil
	cp.config.Cluster.RevokedNodes = nil
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

// SetClusterAdvertiseAddresses replaces the configured advertise addresses.
// Pass nil or empty to fall back to the tracker's observed source IP.
func (cp *ControlPlane) SetClusterAdvertiseAddresses(addrs []string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(addrs) == 0 {
		cp.config.Cluster.AdvertiseAddresses = nil
		return
	}
	out := make([]string, len(addrs))
	copy(out, addrs)
	cp.config.Cluster.AdvertiseAddresses = out
}
