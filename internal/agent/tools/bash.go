package tools

import (
	"bytes"
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

const (
	bashTimeout      = 120 * time.Second
	maxOutputLen     = 32000
)

type BashParams struct {
	Command    string `json:"command"     description:"The shell command to execute."`
	WorkingDir string `json:"working_dir,omitempty" description:"Working directory for the command. Defaults to the agent's working directory."`
}

// NewBashTool creates a tool that executes shell commands.
func NewBashTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"bash",
		bashDescription,
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("command is required"), nil
			}

			dir := workingDir
			if params.WorkingDir != "" {
				dir = params.WorkingDir
			}

			cmdCtx, cancel := context.WithTimeout(ctx, bashTimeout)
			defer cancel()

			cmd := exec.CommandContext(cmdCtx, "bash", "-c", params.Command)
			cmd.Dir = dir

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()

			var output strings.Builder
			if stdout.Len() > 0 {
				s := stdout.String()
				if len(s) > maxOutputLen {
					s = s[:maxOutputLen] + "\n[output truncated]"
				}
				output.WriteString(s)
			}
			if stderr.Len() > 0 {
				s := stderr.String()
				if len(s) > maxOutputLen {
					s = s[:maxOutputLen] + "\n[output truncated]"
				}
				if output.Len() > 0 {
					output.WriteString("\n")
				}
				output.WriteString("STDERR:\n")
				output.WriteString(s)
			}

			if err != nil {
				exitErr := ""
				if e, ok := err.(*exec.ExitError); ok {
					exitErr = fmt.Sprintf(" (exit code %d)", e.ExitCode())
				}
				if output.Len() == 0 {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("command failed%s: %v", exitErr, err)), nil
				}
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("%s\n\ncommand failed%s", output.String(), exitErr)), nil
			}

			if output.Len() == 0 {
				return fantasy.NewTextResponse("(no output)"), nil
			}
			return fantasy.NewTextResponse(output.String()), nil
		},
	)
}
