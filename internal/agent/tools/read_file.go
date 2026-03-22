package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"charm.land/fantasy"
)

//go:embed read_file.md
var readFileDescription string

const maxFileReadLen = 64000

type ReadFileParams struct {
	Path   string `json:"path"             description:"Absolute or relative path to the file to read."`
	Offset int    `json:"offset,omitempty"  description:"Line number to start reading from (1-based). Defaults to 1."`
	Limit  int    `json:"limit,omitempty"   description:"Maximum number of lines to read. Defaults to all lines."`
}

// NewReadFileTool creates a tool that reads file contents with line numbers.
func NewReadFileTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"read_file",
		readFileDescription,
		func(ctx context.Context, params ReadFileParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Path == "" {
				return fantasy.NewTextErrorResponse("path is required"), nil
			}

			path := resolvePath(workingDir, params.Path)
			data, err := os.ReadFile(path)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error reading file: %v", err)), nil
			}

			lines := strings.Split(string(data), "\n")

			// Apply offset (1-based)
			start := 0
			if params.Offset > 0 {
				start = params.Offset - 1
			}
			if start >= len(lines) {
				return fantasy.NewTextResponse("(offset beyond end of file)"), nil
			}
			lines = lines[start:]

			// Apply limit
			if params.Limit > 0 && params.Limit < len(lines) {
				lines = lines[:params.Limit]
			}

			// Format with line numbers
			var sb strings.Builder
			for i, line := range lines {
				lineNum := start + i + 1
				fmt.Fprintf(&sb, "%4d\t%s\n", lineNum, line)
			}

			result := sb.String()
			if len(result) > maxFileReadLen {
				result = result[:maxFileReadLen] + "\n[output truncated]"
			}

			return fantasy.NewTextResponse(result), nil
		},
	)
}
