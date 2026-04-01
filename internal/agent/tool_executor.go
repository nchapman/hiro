package agent

import (
	"context"
	"fmt"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/ipc"
)

// ToolExecutorFromTools creates an ipc.ToolExecutor that dispatches tool calls
// to the given fantasy.AgentTool implementations by name.
func ToolExecutorFromTools(tools []fantasy.AgentTool) ipc.ToolExecutor {
	m := make(map[string]fantasy.AgentTool, len(tools))
	for _, t := range tools {
		m[t.Info().Name] = t
	}
	return &toolExecutor{tools: m}
}

type toolExecutor struct {
	tools map[string]fantasy.AgentTool
}

func (e *toolExecutor) ExecuteTool(ctx context.Context, callID, name, input string) (ipc.ToolResult, error) {
	tool, ok := e.tools[name]
	if !ok {
		return ipc.ToolResult{
			Content: fmt.Sprintf("unknown tool: %s", name),
			IsError: true,
		}, nil
	}

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    callID,
		Name:  name,
		Input: input,
	})
	if err != nil {
		return ipc.ToolResult{}, fmt.Errorf("running tool %s: %w", name, err)
	}

	return ipc.ToolResult{
		Content: resp.Content,
		IsError: resp.Type == "error",
	}, nil
}
