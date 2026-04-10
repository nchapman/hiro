package ipc

import "context"

// AgentWorker is the interface that the control plane uses to communicate
// with an agent worker process. Workers are thin tool-execution sandboxes.
type AgentWorker interface {
	// ExecuteTool runs a named tool in the worker's sandbox and returns the result.
	ExecuteTool(ctx context.Context, callID, name, input string) (ToolResult, error)

	// Shutdown gracefully stops the agent worker process.
	Shutdown(ctx context.Context) error
}

// SecretEnvSetter is an optional interface for AgentWorker implementations
// that support injecting secret environment variables for bash command execution.
type SecretEnvSetter interface {
	SetSecretEnvFn(fn func() []string)
}

// NeedsSecrets reports whether a tool requires secret env var injection.
// Only Bash needs secrets; other tools have no use for them.
func NeedsSecrets(toolName string) bool {
	return toolName == "Bash"
}
