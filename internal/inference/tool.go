package inference

import "charm.land/fantasy"

// Tool wraps a fantasy.AgentTool. All tool builders in the inference
// package return Tool instead of fantasy.AgentTool. The Loop works
// with []Tool internally and extracts []fantasy.AgentTool at the
// fantasy boundary.
type Tool struct {
	fantasy.AgentTool
}

// wrap converts a plain fantasy.AgentTool into a Tool.
func wrap(t fantasy.AgentTool) Tool {
	return Tool{AgentTool: t}
}

// wrapAll converts a slice of plain fantasy.AgentTools into Tools.
func wrapAll(tools []fantasy.AgentTool) []Tool {
	out := make([]Tool, len(tools))
	for i, t := range tools {
		out[i] = Tool{AgentTool: t}
	}
	return out
}

// fantasyTools extracts the underlying fantasy.AgentTool slice for passing
// to the fantasy agent.
func fantasyTools(tools []Tool) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, t := range tools {
		out[i] = t.AgentTool
	}
	return out
}
