package inference

import (
	"context"
	"fmt"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
)

func buildPersonaTools(instanceDir string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		fantasy.NewAgentTool("persona_read",
			"Read your persona file. Contains your identity, tone, and behavioral traits — who you are as an agent.",
			func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				content, err := config.ReadPersonaFile(instanceDir)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to read persona: %v", err)), nil
				}
				if content == "" {
					return fantasy.NewTextResponse("No persona defined yet."), nil
				}
				return fantasy.NewTextResponse(content), nil
			},
		),
		fantasy.NewAgentTool("persona_write",
			"Overwrite your persona file. This defines who you are — your identity, tone, and behavioral traits. Changes appear in your system prompt from the next turn onward.",
			func(ctx context.Context, input struct {
				Content string `json:"content" description:"The full new contents of your persona file."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.Content == "" {
					return fantasy.NewTextErrorResponse("content is required"), nil
				}
				if err := config.WritePersonaFile(instanceDir, input.Content); err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to write persona: %v", err)), nil
				}
				return fantasy.NewTextResponse(
					fmt.Sprintf("Persona updated (%d bytes). Changes will be reflected in your system prompt on the next turn.", len(input.Content))), nil
			},
		),
	}
}
