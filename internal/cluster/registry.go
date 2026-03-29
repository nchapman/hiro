// Package cluster implements leader-worker clustering for distributed
// agent execution. A leader node runs inference and dispatches tool
// calls to worker nodes over gRPC bidirectional streams. Worker nodes
// spawn local agent processes and proxy tool execution back to the leader.
package cluster

import (
	"fmt"
	"sync"
	"time"
)

// NodeID uniquely identifies a node in the cluster. This is a type alias
// for string to stay compatible with ipc.NodeID.
type NodeID = string

// HomeNodeID is the well-known ID for the leader's local node.
const HomeNodeID = "home"

// NodeStatus represents the connection state of a node.
type NodeStatus string

const (
	NodeOnline  NodeStatus = "online"
	NodeOffline NodeStatus = "offline"
)

// NodeInfo describes a node in the cluster.
type NodeInfo struct {
	ID          NodeID     `json:"id"`
	Name        string     `json:"name"`
	Status      NodeStatus `json:"status"`
	IsHome      bool       `json:"is_home"`
	ConnectedAt time.Time  `json:"connected_at"`
	LastSeen    time.Time  `json:"last_seen"`
	Capacity    int        `json:"capacity"`     // max concurrent workers, 0 = unlimited
	ActiveCount int        `json:"active_count"` // currently running workers
}

// NodeRegistry is a thread-safe registry of cluster nodes.
type NodeRegistry struct {
	mu    sync.RWMutex
	nodes map[NodeID]*NodeInfo
}

// NewNodeRegistry creates a new empty node registry.
func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{
		nodes: make(map[NodeID]*NodeInfo),
	}
}

// RegisterHome adds the local leader machine as the home node.
func (r *NodeRegistry) RegisterHome(name string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[HomeNodeID] = &NodeInfo{
		ID:          HomeNodeID,
		Name:        name,
		Status:      NodeOnline,
		IsHome:      true,
		ConnectedAt: now,
		LastSeen:    now,
	}
}

// Register adds or updates a remote node. Returns an error if the ID
// conflicts with the home node.
func (r *NodeRegistry) Register(id NodeID, name string, capacity int) error {
	if id == HomeNodeID {
		return fmt.Errorf("cannot register node with reserved ID %q", HomeNodeID)
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[id] = &NodeInfo{
		ID:          id,
		Name:        name,
		Status:      NodeOnline,
		ConnectedAt: now,
		LastSeen:    now,
		Capacity:    capacity,
	}
	return nil
}

// Unregister removes a node from the registry.
func (r *NodeRegistry) Unregister(id NodeID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, id)
}

// SetOffline marks a node as offline without removing it.
func (r *NodeRegistry) SetOffline(id NodeID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.nodes[id]; ok {
		n.Status = NodeOffline
	}
}

// Touch updates a node's LastSeen timestamp.
func (r *NodeRegistry) Touch(id NodeID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.nodes[id]; ok {
		n.LastSeen = time.Now()
	}
}

// Get returns a copy of a node's info. Returns false if not found.
func (r *NodeRegistry) Get(id NodeID) (NodeInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[id]
	if !ok {
		return NodeInfo{}, false
	}
	return *n, true
}

// List returns a snapshot of all nodes, sorted with home first.
func (r *NodeRegistry) List() []NodeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]NodeInfo, 0, len(r.nodes))
	// Home first.
	if home, ok := r.nodes[HomeNodeID]; ok {
		list = append(list, *home)
	}
	for id, n := range r.nodes {
		if id != HomeNodeID {
			list = append(list, *n)
		}
	}
	return list
}

// OnlineNodes returns nodes with status "online".
func (r *NodeRegistry) OnlineNodes() []NodeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var list []NodeInfo
	for _, n := range r.nodes {
		if n.Status == NodeOnline {
			list = append(list, *n)
		}
	}
	return list
}

// IncrementActive atomically increments the active worker count for a node.
func (r *NodeRegistry) IncrementActive(id NodeID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.nodes[id]; ok {
		n.ActiveCount++
	}
}

// DecrementActive atomically decrements the active worker count for a node.
func (r *NodeRegistry) DecrementActive(id NodeID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.nodes[id]; ok && n.ActiveCount > 0 {
		n.ActiveCount--
	}
}

// Len returns the number of registered nodes.
func (r *NodeRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}
