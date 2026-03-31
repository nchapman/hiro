package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

//go:embed task_output.md
var taskOutputDescription string

type TaskOutputParams struct {
	TaskID string `json:"task_id" description:"The ID of the background task to get output from."`
	Block  *bool  `json:"block,omitempty" description:"Whether to wait for completion. Defaults to true."`
}

// NewTaskOutputTool creates a tool that retrieves output from background tasks.
func NewTaskOutputTool(bgMgr *BackgroundJobManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"TaskOutput",
		taskOutputDescription,
		func(ctx context.Context, params TaskOutputParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.TaskID == "" {
				return fantasy.NewTextErrorResponse("task_id is required"), nil
			}

			job, ok := bgMgr.Get(params.TaskID)
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("no task found with ID: %s", params.TaskID)), nil
			}

			// Default block=true.
			block := params.Block == nil || *params.Block
			if block {
				job.Wait(ctx)
			}

			stdout, stderr, done, err := job.GetOutput()

			stdout = truncateOutput(stdout)
			stderr = truncateOutput(stderr)

			var parts []string
			if stdout != "" {
				parts = append(parts, stdout)
			}
			if stderr != "" {
				parts = append(parts, stderr)
			}

			status := "running"
			if done {
				status = "completed"
				if err != nil {
					if code := exitCode(err); code != 0 {
						parts = append(parts, fmt.Sprintf("Exit code %d", code))
					}
				}
			}

			output := strings.Join(parts, "\n")
			if output == "" {
				output = "(no output)"
			}

			return fantasy.NewTextResponse(fmt.Sprintf("Status: %s\n\n%s", status, output)), nil
		},
	)
}
