package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/fantasy"
)

//go:embed bash.md
var bashDescription string

// maxBashTimeout is the maximum allowed timeout (10 minutes).
const maxBashTimeout = 600000

type BashParams struct {
	Command         string `json:"command"                    description:"The shell command to execute."`
	WorkingDir      string `json:"working_dir,omitempty"      description:"Working directory for the command. Defaults to the agent's working directory."`
	Timeout         int    `json:"timeout,omitempty"          description:"Optional timeout in milliseconds (max 600000). Overrides the default timeout."`
	Description     string `json:"description,omitempty"      description:"Clear, concise description of what this command does."`
	RunInBackground bool   `json:"run_in_background,omitempty" description:"Set to true to run this command in the background. Use TaskOutput to read output later."`
}

// NewBashTool creates a tool that executes shell commands with background job support.
func NewBashTool(workingDir string, bgMgr *BackgroundJobManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"Bash",
		bashDescription,
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("command is required"), nil
			}

			dir := workingDir
			if params.WorkingDir != "" {
				dir = resolvePath(workingDir, params.WorkingDir)
			}

			// Resolve timeout: use param if provided, else default.
			bgTimeout := autoBackgroundAfter
			if params.Timeout > 0 {
				t := params.Timeout
				if t > maxBashTimeout {
					t = maxBashTimeout
				}
				bgTimeout = time.Duration(t) * time.Millisecond
			}

			// Explicit background mode.
			if params.RunInBackground {
				job, err := bgMgr.Start(dir, params.Command)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				job.Description = params.Description

				// Quick check for fast failures (syntax errors, missing commands).
				select {
				case <-job.done:
					bgMgr.Remove(job.ID)
					stdout, stderr, _, execErr := job.GetOutput()
					return formatBashResult(stdout, stderr, execErr), nil
				case <-time.After(100 * time.Millisecond):
				}

				// Truly backgrounded — enable completion notification.
				bgMgr.NotifyOnComplete(job.ID)
				return fantasy.NewTextResponse(
					fmt.Sprintf("Background task started with ID: %s\n\nUse TaskOutput to view output or TaskStop to terminate.", job.ID)), nil
			}

			// Synchronous execution with auto-background on timeout.
			job, err := bgMgr.Start(dir, params.Command)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			job.Description = params.Description

			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.After(bgTimeout)

			for {
				select {
				case <-ticker.C:
					stdout, stderr, done, execErr := job.GetOutput()
					if done {
						bgMgr.Remove(job.ID)
						return formatBashResult(stdout, stderr, execErr), nil
					}
				case <-timeout:
					// Check one last time — job may have finished at the boundary.
					stdout, stderr, done, execErr := job.GetOutput()
					if done {
						bgMgr.Remove(job.ID)
						return formatBashResult(stdout, stderr, execErr), nil
					}
					// Auto-backgrounded — enable completion notification.
					bgMgr.NotifyOnComplete(job.ID)
					return fantasy.NewTextResponse(
						fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground task ID: %s\n\nUse TaskOutput to view output or TaskStop to terminate.", job.ID)), nil
				case <-ctx.Done():
					_ = bgMgr.Kill(job.ID)
					return fantasy.NewTextErrorResponse("command cancelled"), nil
				}
			}
		},
	)
}

func formatBashResult(stdout, stderr string, execErr error) fantasy.ToolResponse {
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
		var e *exec.ExitError
		if errors.As(execErr, &e) {
			exitCode = fmt.Sprintf(" (exit code %d)", e.ExitCode())
		}
		if out.Len() == 0 {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("command failed%s: %v", exitCode, execErr))
		}
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("%s\n\ncommand failed%s", out.String(), exitCode))
	}

	if out.Len() == 0 {
		return fantasy.NewTextResponse("(no output)")
	}
	return fantasy.NewTextResponse(out.String())
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
