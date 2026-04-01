package tools

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/bmatcuk/doublestar/v4"
)

//go:embed glob.md
var globDescription string

type GlobParams struct {
	Pattern string `json:"pattern"        description:"The glob pattern to match files against."`
	Path    string `json:"path,omitempty"  description:"The directory to search in. Defaults to the working directory."`
}

// NewGlobTool creates a tool that finds files matching a glob pattern.
func NewGlobTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"Glob",
		globDescription,
		func(ctx context.Context, params GlobParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPath := workingDir
			if params.Path != "" {
				searchPath = resolvePath(workingDir, params.Path)
			}

			if _, err := os.Stat(searchPath); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error accessing %s: %v", searchPath, err)), nil
			}

			files, truncated, err := globFiles(ctx, params.Pattern, searchPath)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error finding files: %v", err)), nil
			}

			if len(files) == 0 {
				return fantasy.NewTextResponse("No files found"), nil
			}

			// Convert to relative paths and normalize to forward slashes
			for i, f := range files {
				if rel, err := filepath.Rel(searchPath, f); err == nil {
					files[i] = filepath.ToSlash(rel)
				} else {
					files[i] = filepath.ToSlash(f)
				}
			}

			result := strings.Join(files, "\n")
			if truncated {
				result += "\n\n(Results truncated. Use a more specific path or pattern.)"
			}
			return fantasy.NewTextResponse(result), nil
		},
	)
}

type globEntry struct {
	path    string
	modTime int64
}

func globFiles(ctx context.Context, pattern, searchPath string) ([]string, bool, error) {
	// Try ripgrep first
	if cmd := rgGlobCmd(ctx, pattern); cmd != nil {
		cmd.Dir = searchPath
		matches, err := runRgGlob(cmd, searchPath)
		if err == nil {
			truncated := len(matches) > maxGlobResults
			if truncated {
				matches = matches[:maxGlobResults]
			}
			return matches, truncated, nil
		}
		// Fall through to Go implementation
	}

	return globWalk(pattern, searchPath)
}

// runRgGlob parses null-separated ripgrep --files output with streaming.
func runRgGlob(cmd *exec.Cmd, searchRoot string) ([]string, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ripgrep pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ripgrep start: %w", err)
	}

	var entries []globEntry
	scanner := bufio.NewScanner(stdout)
	scanner.Split(splitOnNull)

	for scanner.Scan() {
		p := scanner.Text()
		if p == "" {
			continue
		}
		absPath := p
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(searchRoot, absPath)
		}
		if isHiddenPath(absPath, searchRoot) {
			continue
		}
		info, err := os.Stat(absPath)
		if err != nil {
			continue
		}
		entries = append(entries, globEntry{path: absPath, modTime: info.ModTime().UnixNano()})
		if len(entries) >= maxRgStatEntries {
			break
		}
	}

	// Drain remaining output and wait for process to exit
	io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if len(entries) == 0 && waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, fmt.Errorf("ripgrep: %w", waitErr)
	}

	// Sort by mod time (newest first)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].modTime > entries[j].modTime
	})

	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	return paths, nil
}

// splitOnNull is a bufio.SplitFunc that splits on null bytes.
func splitOnNull(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == 0 {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil // need more data
}

// globWalk is a pure Go fallback using filepath.WalkDir and doublestar.Match.
func globWalk(pattern, searchPath string) ([]string, bool, error) {
	var entries []globEntry

	err := filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		// Skip hidden directories
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != searchPath {
			return filepath.SkipDir
		}

		// Skip common noisy directories.
		if d.IsDir() && excludedDirs[d.Name()] {
			return filepath.SkipDir
		}

		if d.IsDir() {
			return nil
		}

		rel, _ := filepath.Rel(searchPath, path)

		// Use doublestar for proper ** and brace expansion support
		matched, _ := doublestar.Match(pattern, rel)
		if !matched {
			// Also try matching against the basename for simple patterns
			matched, _ = doublestar.Match(pattern, d.Name())
		}

		if matched {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			entries = append(entries, globEntry{
				path:    path,
				modTime: info.ModTime().UnixNano(),
			})
			// Collect one extra to detect truncation
			if len(entries) > maxGlobResults {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}

	// Sort by mod time (newest first)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].modTime > entries[j].modTime
	})

	truncated := len(entries) > maxGlobResults
	if truncated {
		entries = entries[:maxGlobResults]
	}

	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	return paths, truncated, nil
}

// isHiddenPath checks if any component of path (relative to root) starts with a dot.
func isHiddenPath(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
