package ipc

import "context"

// ToolResult is the result of executing a tool.
type ToolResult struct {
	Content string // tool output text
	IsError bool   // true if the tool returned an error
}

// ToolExecutor runs tools in a sandboxed worker process.
// The control plane uses this interface to dispatch tool calls
// to workers when it owns the inference loop.
type ToolExecutor interface {
	// ExecuteTool runs a named tool with the given JSON input and returns the result.
	ExecuteTool(ctx context.Context, callID, name, input string) (ToolResult, error)
}
