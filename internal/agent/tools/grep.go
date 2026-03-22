package tools

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/fantasy"
)

//go:embed grep.md
var grepDescription string

const (
	maxGrepResults    = 100
	maxGrepLineWidth  = 500
	maxMatchesPerFile = 50
	grepTimeout       = 30 * time.Second
	maxRgOutputBytes  = 64 * 1024 * 1024 // 64 MB cap on ripgrep output
)

type GrepParams struct {
	Pattern     string `json:"pattern"              description:"The regex pattern to search for in file contents."`
	Path        string `json:"path,omitempty"        description:"The directory to search in. Defaults to the working directory."`
	Include     string `json:"include,omitempty"     description:"File glob pattern to filter by (e.g. '*.go', '*.{ts,tsx}')."`
	LiteralText bool   `json:"literal_text,omitempty" description:"If true, escape special regex characters to search for exact text. Default false."`
}

type grepMatch struct {
	path     string
	modTime  int64
	lineNum  int
	charNum  int
	lineText string
}

// NewGrepTool creates a tool that searches file contents with regex.
func NewGrepTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"grep",
		grepDescription,
		func(ctx context.Context, params GrepParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPattern := params.Pattern
			if params.LiteralText {
				searchPattern = regexp.QuoteMeta(params.Pattern)
			}

			searchPath := workingDir
			if params.Path != "" {
				searchPath = resolvePath(workingDir, params.Path)
			}

			searchCtx, cancel := context.WithTimeout(ctx, grepTimeout)
			defer cancel()

			matches, truncated, err := grepSearch(searchCtx, searchPattern, searchPath, params.Include)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("error searching files: %v", err)), nil
			}

			// Filter out matches in forbidden paths (e.g. control plane config).
			if len(ForbiddenPaths) > 0 {
				filtered := matches[:0]
				for _, m := range matches {
					abs := m.path
					if !filepath.IsAbs(abs) {
						abs = filepath.Join(searchPath, abs)
					}
					if !IsForbiddenPath(abs) {
						filtered = append(filtered, m)
					}
				}
				matches = filtered
			}

			if len(matches) == 0 {
				return fantasy.NewTextResponse("No matches found"), nil
			}

			var out strings.Builder
			fmt.Fprintf(&out, "Found %d matches\n", len(matches))

			currentFile := ""
			for _, m := range matches {
				if currentFile != m.path {
					if currentFile != "" {
						out.WriteString("\n")
					}
					currentFile = m.path
					fmt.Fprintf(&out, "%s:\n", filepath.ToSlash(m.path))
				}
				line := m.lineText
				if len(line) > maxGrepLineWidth {
					line = line[:maxGrepLineWidth] + "..."
				}
				if m.charNum > 0 {
					fmt.Fprintf(&out, "  Line %d, Col %d: %s\n", m.lineNum, m.charNum, line)
				} else {
					fmt.Fprintf(&out, "  Line %d: %s\n", m.lineNum, line)
				}
			}

			if truncated {
				out.WriteString("\n(Results truncated. Use a more specific path or pattern.)")
			}

			return fantasy.NewTextResponse(out.String()), nil
		},
	)
}

func grepSearch(ctx context.Context, pattern, rootPath, include string) ([]grepMatch, bool, error) {
	matches, err := grepWithRipgrep(ctx, pattern, rootPath, include)
	if err != nil {
		// Only fall back to Go implementation if ripgrep is unavailable.
		// Propagate real errors (bad pattern, permissions, etc).
		if !isRgUnavailable(err) {
			return nil, false, err
		}
		matches, err = grepWithRegex(ctx, pattern, rootPath, include)
		if err != nil {
			return nil, false, err
		}
	}

	// Sort by mod time (newest first), stable to preserve line order within files
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].modTime != matches[j].modTime {
			return matches[i].modTime > matches[j].modTime
		}
		if matches[i].path != matches[j].path {
			return matches[i].path < matches[j].path
		}
		return matches[i].lineNum < matches[j].lineNum
	})

	truncated := len(matches) > maxGrepResults
	if truncated {
		matches = matches[:maxGrepResults]
	}

	return matches, truncated, nil
}

// ripgrep JSON output types
type rgMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
		} `json:"submatches"`
	} `json:"data"`
}

func grepWithRipgrep(ctx context.Context, pattern, path, include string) ([]grepMatch, error) {
	cmd := rgSearchCmd(ctx, pattern, path, include)
	if cmd == nil {
		return nil, errRgUnavailable
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errRgUnavailable
	}
	if err := cmd.Start(); err != nil {
		return nil, errRgUnavailable
	}

	// Limit how much we read from ripgrep to prevent unbounded memory use
	lr := io.LimitReader(stdout, maxRgOutputBytes)
	scanner := bufio.NewScanner(lr)

	var matches []grepMatch
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m rgMatch
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m.Type != "match" {
			continue
		}
		info, err := os.Stat(m.Data.Path.Text)
		if err != nil {
			continue
		}
		charNum := 0
		if len(m.Data.Submatches) > 0 {
			charNum = m.Data.Submatches[0].Start + 1
		}
		matches = append(matches, grepMatch{
			path:     m.Data.Path.Text,
			modTime:  info.ModTime().UnixNano(),
			lineNum:  m.Data.LineNumber,
			charNum:  charNum,
			lineText: strings.TrimSpace(m.Data.Lines.Text),
		})
		// Stop early once we have enough
		if len(matches) >= maxGrepResults*2 {
			break
		}
	}

	// Drain remaining output and wait for process to exit
	io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if len(matches) == 0 && waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, waitErr
	}

	return matches, nil
}

func grepWithRegex(ctx context.Context, pattern, rootPath, include string) ([]grepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includeRe *regexp.Regexp
	if include != "" {
		includeRe, err = regexp.Compile(globToRegex(include))
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	var matches []grepMatch

	err = filepath.WalkDir(rootPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		// Check context cancellation periodically
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Skip hidden directories
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != rootPath {
			return filepath.SkipDir
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "vendor", "dist", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}

		// Skip hidden files
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		// Apply include filter
		if includeRe != nil && !includeRe.MatchString(d.Name()) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		fileMatches := searchTextFile(path, re, info.ModTime().UnixNano())
		matches = append(matches, fileMatches...)
		if len(matches) >= maxGrepResults*2 {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		return nil, err
	}

	return matches, nil
}

// searchTextFile checks if a file is text (no null bytes in the first 512 bytes),
// then searches it for the pattern. Returns all matches up to maxMatchesPerFile.
func searchTextFile(path string, re *regexp.Regexp, modTime int64) []grepMatch {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	// Check for binary content by looking for null bytes in the first 512 bytes
	header := make([]byte, 512)
	hn, err := f.Read(header)
	if err != nil && err != io.EOF {
		return nil
	}
	for _, b := range header[:hn] {
		if b == 0 {
			return nil // binary file
		}
	}

	// Seek back to start to search the full file
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}

	var matches []grepMatch
	scanner := bufio.NewScanner(f)
	// Increase buffer size for long lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for scanner.Scan() {
		n++
		line := scanner.Text()
		if loc := re.FindStringIndex(line); loc != nil {
			matches = append(matches, grepMatch{
				path:     path,
				modTime:  modTime,
				lineNum:  n,
				charNum:  loc[0] + 1,
				lineText: line,
			})
			if len(matches) >= maxMatchesPerFile {
				break
			}
		}
	}
	return matches
}

// globToRegex converts a basename-only glob pattern to a regex for include filtering.
// Only supports *, ?, and {a,b} expansion against file basenames.
func globToRegex(glob string) string {
	// Start with everything escaped, then selectively restore glob semantics
	r := regexp.QuoteMeta(glob)

	// Restore glob wildcards: \* -> .* and \? -> .
	r = strings.ReplaceAll(r, `\*`, ".*")
	r = strings.ReplaceAll(r, `\?`, ".")

	// Restore brace expansion: \{ts\,tsx\} -> (ts|tsx)
	// QuoteMeta escapes {, }, and , — so we need to un-escape them in brace groups
	braceRe := regexp.MustCompile(`\\\{([^}]*)\\\}`)
	r = braceRe.ReplaceAllStringFunc(r, func(match string) string {
		// Strip the escaped braces: \{...\} -> ...
		inner := match[2 : len(match)-2]
		// Replace commas with alternation (commas are not escaped by QuoteMeta)
		inner = strings.ReplaceAll(inner, ",", "|")
		return "(" + inner + ")"
	})

	return "^" + r + "$"
}
