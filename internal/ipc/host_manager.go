package ipc

import "context"

// NodeID uniquely identifies a node in the cluster.
// Empty string or "home" represents the local leader node.
type NodeID = string

// HomeNodeID is the well-known ID for the leader's local node.
const HomeNodeID = "home"

// NodeInfo describes a node in the cluster for external consumers.
type NodeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	IsHome      bool   `json:"is_home"`
	Capacity    int    `json:"capacity"`
	ActiveCount int    `json:"active_count"`
}

// HostManager is the interface that the inference loop uses to manage
// instances. The agent.Manager implements this directly.
type HostManager interface {
	// SpawnEphemeral runs an ephemeral instance and returns its result.
	// nodeID targets a specific cluster node ("" or "home" for local).
	SpawnEphemeral(ctx context.Context, agentName, prompt, parentInstanceID string, nodeID NodeID, onEvent func(ChatEvent) error) (string, error)

	// CreateInstance creates and starts a new child instance in the given mode.
	// nodeID targets a specific cluster node ("" or "home" for local).
	CreateInstance(ctx context.Context, name, parentInstanceID, mode string, nodeID NodeID) (string, error)

	// SendMessage sends a message to a running instance and returns the response.
	SendMessage(ctx context.Context, instanceID, message string, onEvent func(ChatEvent) error) (string, error)

	// StopInstance stops an instance and its entire subtree.
	StopInstance(instanceID string) (InstanceInfo, error)

	// StartInstance restarts a stopped instance (creates a new session within it).
	StartInstance(ctx context.Context, instanceID string) error

	// DeleteInstance stops and permanently removes an instance and its subtree.
	DeleteInstance(instanceID string) error

	// NewSession ends the current session and starts a new one within the instance.
	NewSession(instanceID string) (string, error)

	// IsDescendant reports whether targetID is a descendant of ancestorID
	// in the instance tree.
	IsDescendant(targetID, ancestorID string) bool

	// ListChildInstances returns direct child instances of the given parent.
	ListChildInstances(callerInstanceID string) []InstanceInfo

	// SecretNames returns the names of available secrets.
	SecretNames() []string

	// SecretEnv returns secret env vars (KEY=VALUE pairs).
	SecretEnv() []string

	// ListNodes returns all nodes in the cluster. Returns nil if clustering
	// is not enabled.
	ListNodes() []NodeInfo
}
