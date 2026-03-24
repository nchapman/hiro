package ipc

import "context"

// HostManager is the control-plane-side interface that the gRPC host server
// delegates to. Unlike AgentHost (which is scoped to a single agent's
// perspective), HostManager accepts caller/parent IDs explicitly so a single
// server can multiplex requests from all agent processes.
//
// The agent.Manager satisfies this interface directly.
type HostManager interface {
	// SpawnSession runs an ephemeral session and returns its result.
	SpawnSession(ctx context.Context, agentName, prompt, parentID string, onEvent func(ChatEvent) error) (string, error)

	// CreateSession creates and starts a new persistent child session.
	CreateSession(ctx context.Context, name, parentID string) (string, error)

	// SendMessage sends a message to a running session and returns the response.
	SendMessage(ctx context.Context, sessionID, message string, onEvent func(ChatEvent) error) (string, error)

	// StopSession stops a session and its entire subtree.
	StopSession(sessionID string) (SessionInfo, error)

	// StartSession restarts a stopped session.
	StartSession(ctx context.Context, sessionID string) error

	// DeleteSession stops and permanently removes a session and its subtree.
	DeleteSession(sessionID string) error

	// IsDescendant reports whether targetID is a descendant of ancestorID.
	IsDescendant(targetID, ancestorID string) bool

	// ListChildSessions returns direct child sessions of the given parent.
	ListChildSessions(callerID string) []SessionInfo

	// SecretNames returns the names of available secrets.
	SecretNames() []string

	// SecretEnv returns secret env vars (KEY=VALUE pairs).
	SecretEnv() []string
}
