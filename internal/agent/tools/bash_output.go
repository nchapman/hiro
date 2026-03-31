package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

//go:embed bash_output.md
var bashOutputDescription string

type BashOutputParams struct {
	JobID string `json:"job_id" description:"The ID of the background job to retrieve output from."`
	Wait  bool   `json:"wait,omitempty" description:"If true, block until the job completes before returning output."`
}

// NewBashOutputTool creates a tool that retrieves output from background jobs.
func NewBashOutputTool(bgMgr *BackgroundJobManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"BashOutput",
		bashOutputDescription,
		func(ctx context.Context, params BashOutputParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.JobID == "" {
				return fantasy.NewTextErrorResponse("job_id is required"), nil
			}

			job, ok := bgMgr.Get(params.JobID)
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("background job not found: %s", params.JobID)), nil
			}

			if params.Wait {
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
