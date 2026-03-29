package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"

	"charm.land/fantasy"
)

//go:embed write_file.md
var writeFileDescription string

type WriteFileParams struct {
	Path    string `json:"path"    description:"Absolute or relative path to the file to write."`
	Content string `json:"content" description:"The full content to write to the file."`
}

// NewWriteFileTool creates a tool that writes content to a file.
func NewWriteFileTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"write_file",
		writeFileDescription,
		func(ctx context.Context, params WriteFileParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Path == "" {
				return fantasy.NewTextErrorResponse("path is required"), nil
			}

			path, err := resolveAndConfine(workingDir, params.Path)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Create parent directories if needed
			if err := mkdirFor(path); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error creating directory: %v", err)), nil
			}

			if err := os.WriteFile(path, []byte(params.Content), 0666); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error writing file: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("wrote %d bytes to %s", len(params.Content), path)), nil
		},
	)
}
