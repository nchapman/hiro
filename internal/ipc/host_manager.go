package ipc

import "context"

// HostManager is the interface that the inference loop uses to manage
// instances. The agent.Manager implements this directly.
type HostManager interface {
	// SpawnEphemeral runs an ephemeral instance and returns its result.
	SpawnEphemeral(ctx context.Context, agentName, prompt, parentInstanceID string, onEvent func(ChatEvent) error) (string, error)

	// CreateInstance creates and starts a new child instance in the given mode.
	CreateInstance(ctx context.Context, name, parentInstanceID string, mode string) (string, error)

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
}
