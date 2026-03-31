package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"

	"charm.land/fantasy"
)

//go:embed kill_shell.md
var killShellDescription string

type KillShellParams struct {
	JobID string `json:"job_id" description:"The ID of the background job to terminate."`
}

// NewKillShellTool creates a tool that terminates background jobs.
func NewKillShellTool(bgMgr *BackgroundJobManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"KillShell",
		killShellDescription,
		func(ctx context.Context, params KillShellParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.JobID == "" {
				return fantasy.NewTextErrorResponse("job_id is required"), nil
			}

			if err := bgMgr.Kill(params.JobID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			return fantasy.NewTextResponse(fmt.Sprintf("Background job %s terminated.", params.JobID)), nil
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
