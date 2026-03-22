package ipc

import "context"

// AgentHost is the interface that agent processes use to call back into the
// control plane. It covers manager-level operations: spawning/stopping agents,
// sending messages between agents, and fetching secrets.
type AgentHost interface {
	// SpawnAgent runs an ephemeral subagent to completion and returns its result.
	// The host determines the parent relationship. onDelta receives streaming
	// text deltas during execution.
	SpawnAgent(ctx context.Context, agentName, prompt string, onDelta func(string) error) (string, error)

	// StartAgent starts a persistent child agent and returns its session ID.
	// The host determines the parent relationship.
	StartAgent(ctx context.Context, agentName string) (string, error)

	// SendMessage sends a message to a running agent and returns the response.
	// The host enforces that the target is a descendant. onDelta receives
	// streaming text deltas during execution.
	SendMessage(ctx context.Context, agentID, message string, onDelta func(string) error) (string, error)

	// StopAgent stops a running agent and its entire subtree.
	// The host enforces that the target is a descendant.
	StopAgent(ctx context.Context, agentID string) error

	// ListAgents returns the direct children of the calling agent.
	ListAgents(ctx context.Context) ([]AgentInfo, error)

	// GetSecrets returns secret names (for system prompt) and secret env vars
	// (KEY=VALUE pairs for bash injection).
	GetSecrets(ctx context.Context) (names []string, env []string, err error)
}
