package cluster

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"
)

// ErrPendingApproval is returned by WorkerStream.Connect when the leader
// has not yet approved this node. The worker should retry after a delay.
var ErrPendingApproval = errors.New("pending approval from leader")

// PendingNode represents a worker that connected but is not yet approved.
type PendingNode struct {
	NodeID    string    `json:"node_id"`
	Name      string    `json:"name"`
	Addr      string    `json:"addr"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// PendingRegistry tracks unapproved worker nodes. It persists to a JSON file
// so pending requests survive leader restarts.
type PendingRegistry struct {
	mu       sync.RWMutex
	nodes    map[string]*PendingNode // keyed by NodeID
	filePath string
}

// NewPendingRegistry creates a registry that persists to filePath.
func NewPendingRegistry(filePath string) *PendingRegistry {
	return &PendingRegistry{
		nodes:    make(map[string]*PendingNode),
		filePath: filePath,
	}
}

// Load reads pending nodes from disk. Returns nil if the file doesn't exist.
func (r *PendingRegistry) Load() error {
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var nodes []*PendingNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes = make(map[string]*PendingNode, len(nodes))
	for _, n := range nodes {
		r.nodes[n.NodeID] = n
	}
	return nil
}

// maxPendingNodes caps the registry to prevent unbounded growth from
// unauthenticated connection attempts with unique keypairs.
const maxPendingNodes = 256

// AddOrUpdate adds a pending node or updates LastSeen if it already exists.
// Returns false if the registry is full and the node is new.
func (r *PendingRegistry) AddOrUpdate(node PendingNode) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	if existing, ok := r.nodes[node.NodeID]; ok {
		existing.LastSeen = now
		existing.Name = node.Name
		existing.Addr = node.Addr
		_ = r.saveLocked()
		return true
	}

	if len(r.nodes) >= maxPendingNodes {
		return false
	}

	node.FirstSeen = now
	node.LastSeen = now
	r.nodes[node.NodeID] = &node
	_ = r.saveLocked()
	return true
}

// Remove deletes a pending node.
func (r *PendingRegistry) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[nodeID]; ok {
		delete(r.nodes, nodeID)
		_ = r.saveLocked()
	}
}

// List returns all pending nodes sorted by first-seen time (oldest first).
func (r *PendingRegistry) List() []PendingNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodes := make([]PendingNode, 0, len(r.nodes))
	for _, n := range r.nodes {
		nodes = append(nodes, *n)
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].FirstSeen.Before(nodes[j].FirstSeen)
	})
	return nodes
}

// Get returns a pending node by ID.
func (r *PendingRegistry) Get(nodeID string) (PendingNode, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[nodeID]
	if !ok {
		return PendingNode{}, false
	}
	return *n, true
}

// Count returns the number of pending nodes.
func (r *PendingRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

// saveLocked persists to disk. Caller must hold r.mu.
func (r *PendingRegistry) saveLocked() error {
	nodes := make([]*PendingNode, 0, len(r.nodes))
	for _, n := range r.nodes {
		nodes = append(nodes, n)
	}
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.filePath, data, 0600)
}
