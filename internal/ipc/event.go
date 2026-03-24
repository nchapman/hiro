package ipc

// ChatEvent is a streaming event emitted during agent chat.
// It carries text deltas, tool call details, and tool results
// through the same channel.
type ChatEvent struct {
	Type string `json:"type"` // "delta", "tool_call", "tool_result"

	// Text delta (type == "delta")
	Content string `json:"content,omitempty"`

	// Tool call (type == "tool_call")
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Input      string `json:"input,omitempty"`  // JSON-encoded tool arguments
	Status     string `json:"status,omitempty"` // Human-readable status message (resolved from template)

	// Tool result (type == "tool_result")
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}
