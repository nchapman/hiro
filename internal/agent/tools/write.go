package tools

import (
	"context"
	_ "embed"
	"fmt"

	"charm.land/fantasy"
)

//go:embed write.md
var writeDescription string

type WriteParams struct {
	FilePath string `json:"file_path" description:"Absolute or relative path to the file to write."`
	Content  string `json:"content"   description:"The full content to write to the file."`
}

// NewWriteTool creates a tool that writes content to a file.
func NewWriteTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"Write",
		writeDescription,
		func(ctx context.Context, params WriteParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			path, err := resolveAndConfine(workingDir, params.FilePath)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Create parent directories if needed
			if err := mkdirFor(path); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error creating directory: %v", err)), nil
			}

			if err := atomicWriteFile(path, []byte(params.Content), 0o666); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error writing file: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("wrote %d bytes to %s", len(params.Content), path)), nil
		},
	)
}
