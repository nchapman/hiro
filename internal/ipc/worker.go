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

	// ConfigChanged pushes resolved structural config to the agent.
	// Called by the control plane when agent definitions or control plane
	// config change. The agent applies the update on its next turn.
	ConfigChanged(ctx context.Context, update ConfigUpdate) error

	// ExecuteTool runs a named tool in the worker's sandbox and returns the result.
	// Used when the control plane owns the inference loop.
	ExecuteTool(ctx context.Context, callID, name, input string) (ToolResult, error)
}
