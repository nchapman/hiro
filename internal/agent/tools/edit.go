package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
)

//go:embed edit.md
var editDescription string

type EditParams struct {
	FilePath   string `json:"file_path"              description:"The absolute path to the file to modify."`
	OldString  string `json:"old_string"             description:"The exact text to find and replace. Must match exactly including whitespace and indentation."`
	NewString  string `json:"new_string"             description:"The replacement text."`
	ReplaceAll bool   `json:"replace_all,omitempty"   description:"Replace all occurrences of old_string. Default false."`
}

// NewEditTool creates a tool that performs surgical find-and-replace edits on files.
func NewEditTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"edit",
		editDescription,
		func(ctx context.Context, params EditParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			filePath := resolvePath(workingDir, params.FilePath)
			if IsForbiddenPath(filePath) {
				return fantasy.NewTextErrorResponse("this file is managed by the operator and is not available to agents"), nil
			}

			// Create new file: old_string empty, new_string has content
			if params.OldString == "" {
				return createFile(filePath, params.NewString)
			}

			// Edit existing file
			return editFile(filePath, params.OldString, params.NewString, params.ReplaceAll)
		},
	)
}

func createFile(filePath, content string) (fantasy.ToolResponse, error) {
	if _, err := os.Stat(filePath); err == nil {
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("file already exists: %s (use old_string + new_string to edit it)", filePath)), nil
	}

	if dir := filepath.Dir(filePath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("error creating directory: %v", err)), nil
		}
	}

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("error creating file: %v", err)), nil
	}

	return fantasy.NewTextResponse(fmt.Sprintf("Created file: %s", filePath)), nil
}

func editFile(filePath, oldString, newString string, replaceAll bool) (fantasy.ToolResponse, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("error accessing file: %v", err)), nil
	}
	fileMode := info.Mode()

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("error reading file: %v", err)), nil
	}

	content := string(data)

	if replaceAll {
		count := strings.Count(content, oldString)
		if count == 0 {
			return fantasy.NewTextErrorResponse(
				"old_string not found in file. Make sure it matches exactly, including whitespace and line breaks."), nil
		}
		newContent := strings.ReplaceAll(content, oldString, newString)
		if content == newContent {
			return fantasy.NewTextErrorResponse("new content is the same as old content. No changes made."), nil
		}

		if err := os.WriteFile(filePath, []byte(newContent), fileMode); err != nil {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("error writing file: %v", err)), nil
		}

		return fantasy.NewTextResponse(
			fmt.Sprintf("Replaced %d occurrence(s) in %s", count, filePath)), nil
	}

	// Single replacement — require unique match
	index := strings.Index(content, oldString)
	if index == -1 {
		return fantasy.NewTextErrorResponse(
			"old_string not found in file. Make sure it matches exactly, including whitespace and line breaks."), nil
	}

	lastIndex := strings.LastIndex(content, oldString)
	if index != lastIndex {
		return fantasy.NewTextErrorResponse(
			"old_string appears multiple times in the file. Provide more context to ensure a unique match, or set replace_all to true."), nil
	}

	newContent := content[:index] + newString + content[index+len(oldString):]
	if err := os.WriteFile(filePath, []byte(newContent), fileMode); err != nil {
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("error writing file: %v", err)), nil
	}

	return fantasy.NewTextResponse(fmt.Sprintf("Edited %s", filePath)), nil
}
