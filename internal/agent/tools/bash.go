package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/fantasy"
)

//go:embed bash.md
var bashDescription string

type BashParams struct {
	Command         string `json:"command"                    description:"The shell command to execute."`
	WorkingDir      string `json:"working_dir,omitempty"      description:"Working directory for the command. Defaults to the agent's working directory."`
	RunInBackground bool   `json:"run_in_background,omitempty" description:"Set to true to run this command in a background job. Use job_output to read output later."`
}

// NewBashTool creates a tool that executes shell commands with background job support.
func NewBashTool(workingDir string, bgMgr *BackgroundJobManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"bash",
		bashDescription,
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("command is required"), nil
			}

			dir := workingDir
			if params.WorkingDir != "" {
				dir = resolvePath(workingDir, params.WorkingDir)
			}

			// Explicit background mode.
			if params.RunInBackground {
				job, err := bgMgr.Start(dir, params.Command)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}

				// Quick check for fast failures (syntax errors, missing commands).
				select {
				case <-job.done:
					bgMgr.Remove(job.ID)
					stdout, stderr, _, execErr := job.GetOutput()
					return formatBashResult(stdout, stderr, execErr)
				case <-time.After(100 * time.Millisecond):
				}

				return fantasy.NewTextResponse(
					fmt.Sprintf("Background job started with ID: %s\n\nUse job_output to view output or job_kill to terminate.", job.ID)), nil
			}

			// Synchronous execution with auto-background on timeout.
			job, err := bgMgr.Start(dir, params.Command)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.After(autoBackgroundAfter)

			for {
				select {
				case <-ticker.C:
					stdout, stderr, done, execErr := job.GetOutput()
					if done {
						bgMgr.Remove(job.ID)
						return formatBashResult(stdout, stderr, execErr)
					}
				case <-timeout:
					// Check one last time — job may have finished at the boundary.
					stdout, stderr, done, execErr := job.GetOutput()
					if done {
						bgMgr.Remove(job.ID)
						return formatBashResult(stdout, stderr, execErr)
					}
					return fantasy.NewTextResponse(
						fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground job ID: %s\n\nUse job_output to view output or job_kill to terminate.", job.ID)), nil
				case <-ctx.Done():
					_ = bgMgr.Kill(job.ID)
					return fantasy.NewTextErrorResponse("command cancelled"), nil
				}
			}
		},
	)
}

func formatBashResult(stdout, stderr string, execErr error) (fantasy.ToolResponse, error) {
	stdout = truncateOutput(stdout)
	stderr = truncateOutput(stderr)

	var out strings.Builder
	if stdout != "" {
		out.WriteString(stdout)
	}
	if stderr != "" {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString("STDERR:\n")
		out.WriteString(stderr)
	}

	if execErr != nil {
		exitCode := ""
		if e, ok := execErr.(*exec.ExitError); ok {
			exitCode = fmt.Sprintf(" (exit code %d)", e.ExitCode())
		}
		if out.Len() == 0 {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("command failed%s: %v", exitCode, execErr)), nil
		}
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("%s\n\ncommand failed%s", out.String(), exitCode)), nil
	}

	if out.Len() == 0 {
		return fantasy.NewTextResponse("(no output)"), nil
	}
	return fantasy.NewTextResponse(out.String()), nil
}

func truncateOutput(s string) string {
	if len(s) <= maxOutputLen {
		return s
	}
	half := maxOutputLen / 2
	start := s[:half]
	end := s[len(s)-half:]
	skipped := strings.Count(s[half:len(s)-half], "\n")
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, skipped, end)
}
