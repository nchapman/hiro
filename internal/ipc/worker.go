package ipc

import "context"

// AgentWorker is the interface that the control plane uses to communicate
// with an agent worker process. Each agent process implements this.
type AgentWorker interface {
	// Chat sends a message to the agent and returns its response.
	// onDelta receives streaming text deltas during execution.
	Chat(ctx context.Context, message string, onDelta func(string) error) (string, error)

	// Shutdown gracefully stops the agent worker process.
	Shutdown(ctx context.Context) error
}
