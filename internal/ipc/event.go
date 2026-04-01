package ipc

// ChatEvent is a streaming event emitted during agent chat.
// It carries text deltas, tool call details, tool results,
// and reasoning/thinking content through the same channel.
type ChatEvent struct {
	Type string `json:"type"` // "delta", "tool_call", "tool_result", "reasoning_start", "reasoning_delta", "reasoning_end"

	// Text delta (type == "delta") or reasoning delta (type == "reasoning_delta")
	Content string `json:"content,omitempty"`

	// Tool call (type == "tool_call")
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Input      string `json:"input,omitempty"`  // JSON-encoded tool arguments
	Status     string `json:"status,omitempty"` // Human-readable status message (resolved from template)

	// Tool result (type == "tool_result")
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`

	// Meta flag — when true, the event is visible to the model but hidden
	// from the user's chat transcript. Used for task completion notifications,
	// system diagnostics, and other internal signals.
	IsMeta bool `json:"is_meta,omitempty"`
}
