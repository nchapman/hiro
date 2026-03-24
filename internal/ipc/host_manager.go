package ipc

import "context"

// HostManager is the control-plane-side interface that the gRPC host server
// delegates to. Unlike AgentHost (which is scoped to a single agent's
// perspective), HostManager accepts caller/parent IDs explicitly so a single
// server can multiplex requests from all agent processes.
//
// The agent.Manager satisfies this interface directly.
type HostManager interface {
	// SpawnSubagent runs an ephemeral subagent and returns its result.
	SpawnSubagent(ctx context.Context, agentName, prompt, parentID string, onEvent func(ChatEvent) error) (string, error)

	// StartAgent starts a persistent child agent.
	StartAgent(ctx context.Context, name, parentID string) (string, error)

	// SendMessage sends a message to a running agent and returns the response.
	SendMessage(ctx context.Context, agentID, message string, onEvent func(ChatEvent) error) (string, error)

	// StopAgent stops an agent and its entire subtree.
	StopAgent(agentID string) (AgentInfo, error)

	// IsDescendant reports whether targetID is a descendant of ancestorID.
	IsDescendant(targetID, ancestorID string) bool

	// ListChildren returns direct children of the given agent.
	ListChildren(callerID string) []AgentInfo

	// SecretNames returns the names of available secrets.
	SecretNames() []string

	// SecretEnv returns secret env vars (KEY=VALUE pairs).
	SecretEnv() []string
}
