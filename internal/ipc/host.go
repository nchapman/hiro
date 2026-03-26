package ipc

import "context"

// AgentHost is the interface that agent processes use to call back into the
// control plane. It covers session-level operations: creating/stopping sessions,
// sending messages between sessions, and fetching secrets.
type AgentHost interface {
	// SpawnSession runs an ephemeral session to completion and returns its result.
	// The host determines the parent relationship. onEvent receives streaming
	// events (text deltas, tool calls, tool results).
	SpawnSession(ctx context.Context, agentName, prompt string, onEvent func(ChatEvent) error) (string, error)

	// CreateSession creates and starts a new child session from an agent
	// definition in the given mode. Returns the session ID. The host
	// determines the parent.
	CreateSession(ctx context.Context, agentName, mode string) (string, error)

	// SendMessage sends a message to a running session and returns the response.
	// The host enforces that the target is a descendant. onEvent receives
	// streaming events (text deltas, tool calls, tool results).
	SendMessage(ctx context.Context, sessionID, message string, onEvent func(ChatEvent) error) (string, error)

	// StopSession stops a session and its entire subtree.
	// Persistent sessions are soft-stopped (kept visible). The host enforces
	// that the target is a descendant.
	StopSession(ctx context.Context, sessionID string) error

	// StartSession restarts a stopped session. The host enforces descendant scoping.
	StartSession(ctx context.Context, sessionID string) error

	// DeleteSession stops and permanently removes a session and its subtree,
	// including session directories. The host enforces descendant scoping.
	DeleteSession(ctx context.Context, sessionID string) error

	// ListSessions returns the direct child sessions of the calling agent.
	ListSessions(ctx context.Context) ([]SessionInfo, error)

	// GetSecrets returns secret names (for system prompt) and secret env vars
	// (KEY=VALUE pairs for bash injection).
	GetSecrets(ctx context.Context) (names []string, env []string, err error)
}
