package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"charm.land/fantasy"
)

//go:embed multiedit.md
var multieditDescription string

// MultiEditOperation describes a single find-and-replace within a multiedit batch.
type MultiEditOperation struct {
	OldString  string `json:"old_string"             description:"The text to replace."`
	NewString  string `json:"new_string"             description:"The replacement text."`
	ReplaceAll bool   `json:"replace_all,omitempty"   description:"Replace all occurrences of old_string. Default false."`
}

// MultiEditParams is the top-level parameter for the multiedit tool.
type MultiEditParams struct {
	FilePath string               `json:"file_path" description:"Absolute or relative path to the file to modify."`
	Edits    []MultiEditOperation `json:"edits"     description:"Array of edit operations to apply sequentially."`
}

// NewMultiEditTool creates a tool that applies multiple edits to a single file.
func NewMultiEditTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"multiedit_file",
		multieditDescription,
		func(ctx context.Context, params MultiEditParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}
			if len(params.Edits) == 0 {
				return fantasy.NewTextErrorResponse("at least one edit operation is required"), nil
			}

			filePath, err := resolveAndConfine(workingDir, params.FilePath)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Validate: only the first edit may have empty old_string (file creation).
			for i, e := range params.Edits {
				if i > 0 && e.OldString == "" {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("edit %d: only the first edit can have empty old_string (for file creation)", i+1)), nil
				}
			}

			// File creation path.
			if params.Edits[0].OldString == "" {
				return multiEditCreate(filePath, params.Edits)
			}

			return multiEditExisting(filePath, params.Edits)
		},
	)
}

// multiEditCreate handles the case where the first edit creates a new file.
func multiEditCreate(filePath string, edits []MultiEditOperation) (fantasy.ToolResponse, error) {
	if _, err := os.Stat(filePath); err == nil {
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("file already exists: %s (use old_string + new_string to edit it)", filePath)), nil
	}

	if err := mkdirFor(filePath); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error creating directory: %v", err)), nil
	}

	content := edits[0].NewString

	// Apply remaining edits.
	var failed []string
	for i := 1; i < len(edits); i++ {
		result, err := applyEdit(content, edits[i])
		if err != nil {
			failed = append(failed, fmt.Sprintf("edit %d: %s", i+1, err))
			continue
		}
		content = result
	}

	if err := os.WriteFile(filePath, []byte(content), 0666); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error writing file: %v", err)), nil
	}

	return multiEditResponse(filePath, len(edits), failed)
}

// multiEditExisting applies a batch of edits to an existing file.
func multiEditExisting(filePath string, edits []MultiEditOperation) (fantasy.ToolResponse, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error accessing file: %v", err)), nil
	}
	if info.IsDir() {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("path is a directory: %s", filePath)), nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error reading file: %v", err)), nil
	}

	content := string(data)
	original := content

	var failed []string
	for i, e := range edits {
		result, err := applyEdit(content, e)
		if err != nil {
			failed = append(failed, fmt.Sprintf("edit %d: %s", i+1, err))
			continue
		}
		content = result
	}

	if content == original {
		if len(failed) > 0 {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("no changes made — all %d edit(s) failed:\n%s", len(failed), strings.Join(failed, "\n"))), nil
		}
		return fantasy.NewTextErrorResponse("no changes made — edits resulted in identical content"), nil
	}

	if err := os.WriteFile(filePath, []byte(content), info.Mode()); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error writing file: %v", err)), nil
	}

	return multiEditResponse(filePath, len(edits), failed)
}

// applyEdit applies a single edit operation to content.
func applyEdit(content string, e MultiEditOperation) (string, error) {
	if e.OldString == "" && e.NewString == "" {
		return content, nil
	}
	if e.OldString == "" {
		return "", fmt.Errorf("old_string cannot be empty for content replacement")
	}

	if e.ReplaceAll {
		if !strings.Contains(content, e.OldString) {
			return "", fmt.Errorf("old_string not found")
		}
		return strings.ReplaceAll(content, e.OldString, e.NewString), nil
	}

	idx := strings.Index(content, e.OldString)
	if idx == -1 {
		return "", fmt.Errorf("old_string not found")
	}
	if strings.LastIndex(content, e.OldString) != idx {
		return "", fmt.Errorf("old_string appears multiple times — provide more context or set replace_all")
	}

	return content[:idx] + e.NewString + content[idx+len(e.OldString):], nil
}

func multiEditResponse(filePath string, total int, failed []string) (fantasy.ToolResponse, error) {
	applied := total - len(failed)
	if len(failed) == 0 {
		return fantasy.NewTextResponse(
			fmt.Sprintf("Applied %d edit(s) to %s", applied, filePath)), nil
	}
	msg := fmt.Sprintf("Applied %d of %d edit(s) to %s (%d failed):\n%s",
		applied, total, filePath, len(failed), strings.Join(failed, "\n"))
	return fantasy.NewTextErrorResponse(msg), nil
}
