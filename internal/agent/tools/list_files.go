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

//go:embed list_files.md
var listFilesDescription string

const maxListEntries = 500

type ListFilesParams struct {
	Path    string `json:"path"              description:"Directory path to list. Defaults to the current working directory."`
	Pattern string `json:"pattern,omitempty"  description:"Glob pattern to filter results (e.g. '*.go', '**/*.ts'). Defaults to all files."`
}

// NewListFilesTool creates a tool that lists directory contents.
func NewListFilesTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"list_files",
		listFilesDescription,
		func(ctx context.Context, params ListFilesParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			dir := workingDir
			if params.Path != "" {
				dir = params.Path
			}

			info, err := os.Stat(dir)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error accessing %s: %v", dir, err)), nil
			}
			if !info.IsDir() {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("%s is not a directory", dir)), nil
			}

			var entries []string
			count := 0
			truncated := false

			err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil // skip errors
				}

				// Skip hidden directories (but not the root)
				if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != dir {
					return filepath.SkipDir
				}

				// Skip node_modules, vendor, etc.
				if d.IsDir() {
					switch d.Name() {
					case "node_modules", "vendor", "dist", "__pycache__", ".git":
						return filepath.SkipDir
					}
				}

				rel, _ := filepath.Rel(dir, path)
				if rel == "." {
					return nil
				}

				// Apply pattern filter
				if params.Pattern != "" {
					matched, _ := filepath.Match(params.Pattern, d.Name())
					if !matched && d.IsDir() {
						return nil // keep walking dirs even if they don't match
					}
					if !matched {
						return nil
					}
				}

				suffix := ""
				if d.IsDir() {
					suffix = "/"
				}

				count++
				if count > maxListEntries {
					truncated = true
					return filepath.SkipAll
				}
				entries = append(entries, rel+suffix)
				return nil
			})

			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error listing directory: %v", err)), nil
			}

			if len(entries) == 0 {
				return fantasy.NewTextResponse("(empty directory)"), nil
			}

			result := strings.Join(entries, "\n")
			if truncated {
				result += fmt.Sprintf("\n[truncated — showing %d of %d+ entries]", maxListEntries, count)
			}
			return fantasy.NewTextResponse(result), nil
		},
	)
}
