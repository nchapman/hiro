package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"

	"charm.land/fantasy"
)

//go:embed task_stop.md
var taskStopDescription string

type TaskStopParams struct {
	TaskID string `json:"task_id" description:"The ID of the background task to stop."`
}

// NewTaskStopTool creates a tool that terminates background tasks.
func NewTaskStopTool(bgMgr *BackgroundJobManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"TaskStop",
		taskStopDescription,
		func(ctx context.Context, params TaskStopParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.TaskID == "" {
				return fantasy.NewTextErrorResponse("task_id is required"), nil
			}

			if err := bgMgr.Kill(params.TaskID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			return fantasy.NewTextResponse(fmt.Sprintf("Successfully stopped task: %s", params.TaskID)), nil
		},
	)
}

// exitCode extracts the exit code from an exec error, or returns -1.
func exitCode(err error) int {
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode()
	}
	return -1
}
