package inference

import (
	"context"
	_ "embed"

	"charm.land/fantasy"
)

//go:embed notify.md
var notifyDescription string

func buildNotifyTool(notifications *NotificationQueue) Tool {
	return wrap(fantasy.NewAgentTool("Notify",
		notifyDescription,
		func(ctx context.Context, input struct {
			Message string `json:"message" description:"The message to deliver to the user's primary conversation."`
		}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Message == "" {
				return fantasy.NewTextErrorResponse("message cannot be empty"), nil
			}
			notifications.Push(Notification{
				Content: input.Message,
				Source:  "scheduled-task",
			})
			return fantasy.NewTextResponse("Notification delivered."), nil
		},
	))
}
