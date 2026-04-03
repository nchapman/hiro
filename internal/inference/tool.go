package inference

import "charm.land/fantasy"

// Tool wraps a fantasy.AgentTool with optional system prompt context.
// All tool builders in the inference package return Tool instead of
// fantasy.AgentTool. The Loop works with []Tool internally and extracts
// []fantasy.AgentTool at the fantasy boundary.
type Tool struct {
	fantasy.AgentTool
	Context *ToolContext // if non-nil, contributed to the system prompt
}

// wrap converts a plain fantasy.AgentTool into a Tool with no context.
func wrap(t fantasy.AgentTool) Tool {
	return Tool{AgentTool: t}
}

// wrapAll converts a slice of plain fantasy.AgentTools into Tools with no context.
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

// collectToolContext gathers and deduplicates ToolContext from the tool set.
// Entries with identical heading and content are deduplicated.
func collectToolContext(tools []Tool) []ToolContext {
	type key struct{ heading, content string }
	var result []ToolContext
	seen := make(map[key]bool)
	for _, t := range tools {
		if t.Context == nil {
			continue
		}
		k := key{t.Context.Heading, t.Context.Content}
		if seen[k] {
			continue
		}
		seen[k] = true
		result = append(result, *t.Context)
	}
	return result
}
