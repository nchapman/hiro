package ipc

import "context"

// AgentWorker is the interface that the control plane uses to communicate
// with an agent worker process. Each agent process implements this.
type AgentWorker interface {
	// Chat sends a message to the agent and returns its response.
	// onEvent receives streaming events (text deltas, tool calls, tool results).
	Chat(ctx context.Context, message string, onEvent func(ChatEvent) error) (string, error)

	// Shutdown gracefully stops the agent worker process.
	Shutdown(ctx context.Context) error
}
