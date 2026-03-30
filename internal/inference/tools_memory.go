package inference

import (
	"context"
	"fmt"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
)

func buildMemoryTools(instanceDir string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		fantasy.NewAgentTool("memory_read",
			"Read your persistent memory file. Contains facts and context you've chosen to retain across conversations. Read before writing to avoid overwriting entries.",
			func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				content, err := config.ReadMemoryFile(instanceDir)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to read memory: %v", err)), nil
				}
				if content == "" {
					return fantasy.NewTextResponse("No memories stored yet."), nil
				}
				return fantasy.NewTextResponse(content), nil
			},
		),
		fantasy.NewAgentTool("memory_write",
			"Overwrite your persistent memory file. Read first to avoid losing existing entries. Changes appear in your system prompt from the next turn onward.",
			func(ctx context.Context, input struct {
				Content string `json:"content" description:"The full new contents of your memory file."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.Content == "" {
					return fantasy.NewTextErrorResponse("content is required"), nil
				}
				if err := config.WriteMemoryFile(instanceDir, input.Content); err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to write memory: %v", err)), nil
				}
				return fantasy.NewTextResponse(
					fmt.Sprintf("Memory updated (%d bytes). Changes will be reflected in your system prompt on the next turn.", len(input.Content))), nil
			},
		),
	}
}
