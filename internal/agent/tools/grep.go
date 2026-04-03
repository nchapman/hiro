package tools

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"charm.land/fantasy"
)

//go:embed grep.md
var grepDescription string

type GrepParams struct {
	Pattern     string `json:"pattern"                description:"The regular expression pattern to search for in file contents."`
	Path        string `json:"path,omitempty"          description:"File or directory to search in. Defaults to the working directory."`
	Glob        string `json:"glob,omitempty"          description:"Glob pattern to filter files (e.g. '*.js', '*.{ts,tsx}')."`
	Type        string `json:"type,omitempty"          description:"File type to search (rg --type). Common types: js, py, rust, go, java."`
	OutputMode  string `json:"output_mode,omitempty"   description:"Output mode: 'content' shows matching lines, 'files_with_matches' shows only file paths, 'count' shows match counts. Default: 'files_with_matches'."`
	ContextA    int    `json:"A,omitempty"             description:"Number of lines to show after each match. Requires output_mode 'content'."`
	ContextB    int    `json:"B,omitempty"             description:"Number of lines to show before each match. Requires output_mode 'content'."`
	ContextC    int    `json:"C,omitempty"             description:"Number of lines to show before and after each match. Requires output_mode 'content'."`
	Context     int    `json:"context,omitempty"       description:"Alias for C."`
	LineNumbers *bool  `json:"n,omitempty"             description:"Show line numbers in output. Requires output_mode 'content'. Default: true."`
	CaseInsens  bool   `json:"i,omitempty"             description:"Case insensitive search."`
	HeadLimit   *int   `json:"head_limit,omitempty"    description:"Limit output to first N lines/entries. Default: 250. Pass 0 for unlimited."`
	Offset      int    `json:"offset,omitempty"        description:"Skip first N lines/entries before applying head_limit."`
	Multiline   bool   `json:"multiline,omitempty"     description:"Enable multiline mode where . matches newlines and patterns can span lines."`
	LiteralText bool   `json:"literal_text,omitempty"  description:"If true, escape special regex characters to search for exact text."`
}

// NewGrepTool creates a tool that searches file contents with regex.
func NewGrepTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		"Grep",
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

			mode := params.OutputMode
			if mode == "" {
				mode = "files_with_matches"
			}

			searchCtx, cancel := context.WithTimeout(ctx, grepTimeout)
			defer cancel()

			switch mode {
			case "content":
				return grepContent(searchCtx, searchPattern, searchPath, workingDir, params)
			case "files_with_matches":
				return grepFilesWithMatches(searchCtx, searchPattern, searchPath, workingDir, params)
			case "count":
				return grepCount(searchCtx, searchPattern, searchPath, workingDir, params)
			default:
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("invalid output_mode %q: must be content, files_with_matches, or count", mode)), nil
			}
		},
	)
}

// defaultHeadLimit is the default limit on results when head_limit is not specified.
const defaultHeadLimit = 250

func effectiveHeadLimit(p GrepParams) int {
	if p.HeadLimit != nil {
		if *p.HeadLimit == 0 {
			return 0 // unlimited
		}
		return *p.HeadLimit
	}
	return defaultHeadLimit
}

func showLineNumbers(p GrepParams) bool {
	if p.LineNumbers != nil {
		return *p.LineNumbers
	}
	return true
}

// applyPagination slices a string slice by offset and head_limit.
// Returns the sliced items and whether results were truncated.
func applyPagination(items []string, offset, limit int) ([]string, bool) {
	if offset > 0 {
		if offset >= len(items) {
			return nil, false
		}
		items = items[offset:]
	}
	if limit > 0 && limit < len(items) {
		return items[:limit], true
	}
	return items, false
}

// resolveContext returns the effective context value, with -C / context overriding -A/-B.
func resolveContext(p GrepParams) (before, after int) {
	c := p.Context
	if p.ContextC > 0 {
		c = p.ContextC
	}
	if c > 0 {
		return c, c
	}
	return p.ContextB, p.ContextA
}

func relativizePath(absPath, workingDir string) string {
	rel, err := filepath.Rel(workingDir, absPath)
	if err != nil {
		return absPath
	}
	return filepath.ToSlash(rel)
}

// --- Content mode ---

func grepContent(ctx context.Context, pattern, searchPath, workingDir string, params GrepParams) (fantasy.ToolResponse, error) {
	lines, err := runRgText(ctx, pattern, searchPath, params)
	if err != nil {
		// Try Go fallback if ripgrep unavailable
		if isRgUnavailable(err) {
			return grepContentFallback(ctx, pattern, searchPath, workingDir, params)
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
	}

	if len(lines) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}

	// Relativize paths in output lines
	for i, line := range lines {
		lines[i] = relativizeContentLine(line, searchPath, workingDir)
	}

	limit := effectiveHeadLimit(params)
	sliced, truncated := applyPagination(lines, params.Offset, limit)

	result := strings.Join(sliced, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n(Results truncated at %d lines. Use head_limit and offset for pagination.)", limit)
	}
	return fantasy.NewTextResponse(result), nil
}

func relativizeContentLine(line, searchPath, workingDir string) string {
	// Lines from rg look like /abs/path:linenum:content or /abs/path-content
	// We need to replace the absolute path prefix with a relative one.
	if !filepath.IsAbs(searchPath) {
		return line
	}
	// Find the first colon that separates path from content
	for i, ch := range line {
		if ch == ':' || ch == '-' {
			prefix := line[:i]
			if filepath.IsAbs(prefix) {
				rel := relativizePath(prefix, workingDir)
				return rel + line[i:]
			}
			break
		}
	}
	return line
}

func grepContentFallback(ctx context.Context, pattern, searchPath, workingDir string, params GrepParams) (fantasy.ToolResponse, error) {
	matches, err := grepWithRegex(ctx, pattern, searchPath, params.Glob)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
	}

	if len(matches) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}

	// Sort by mod time, then by file, then by line
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].modTime != matches[j].modTime {
			return matches[i].modTime > matches[j].modTime
		}
		if matches[i].path != matches[j].path {
			return matches[i].path < matches[j].path
		}
		return matches[i].lineNum < matches[j].lineNum
	})

	var lines []string
	showNums := showLineNumbers(params)
	currentFile := ""
	for _, m := range matches {
		relPath := relativizePath(m.path, workingDir)
		if currentFile != relPath {
			if currentFile != "" {
				lines = append(lines, "")
			}
			currentFile = relPath
			lines = append(lines, relPath+":")
		}
		text := m.lineText
		if len(text) > maxGrepLineWidth {
			text = text[:maxGrepLineWidth] + "..."
		}
		if showNums {
			lines = append(lines, fmt.Sprintf("  %d: %s", m.lineNum, text))
		} else {
			lines = append(lines, fmt.Sprintf("  %s", text))
		}
	}

	limit := effectiveHeadLimit(params)
	sliced, truncated := applyPagination(lines, params.Offset, limit)

	result := strings.Join(sliced, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n(Results truncated at %d lines. Use head_limit and offset for pagination.)", limit)
	}
	return fantasy.NewTextResponse(result), nil
}

// --- Files with matches mode ---

func grepFilesWithMatches(ctx context.Context, pattern, searchPath, workingDir string, params GrepParams) (fantasy.ToolResponse, error) {
	lines, err := runRgFilesList(ctx, pattern, searchPath, params)
	if err != nil {
		if isRgUnavailable(err) {
			return grepFilesWithMatchesFallback(ctx, pattern, searchPath, workingDir, params)
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
	}

	if len(lines) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}

	// Stat and sort by mtime
	type fileEntry struct {
		path    string
		modTime int64
	}
	var entries []fileEntry
	for _, f := range lines {
		absPath := f
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(searchPath, f)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			continue
		}
		entries = append(entries, fileEntry{path: absPath, modTime: info.ModTime().UnixNano()})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].modTime > entries[j].modTime
	})

	filePaths := make([]string, len(entries))
	for i, e := range entries {
		filePaths[i] = relativizePath(e.path, workingDir)
	}

	limit := effectiveHeadLimit(params)
	sliced, truncated := applyPagination(filePaths, params.Offset, limit)

	result := strings.Join(sliced, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n(Results truncated at %d entries. Use head_limit and offset for pagination.)", limit)
	}
	return fantasy.NewTextResponse(result), nil
}

func grepFilesWithMatchesFallback(ctx context.Context, pattern, searchPath, workingDir string, params GrepParams) (fantasy.ToolResponse, error) {
	matches, err := grepWithRegex(ctx, pattern, searchPath, params.Glob)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
	}
	if len(matches) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}

	// Deduplicate files, sort by mtime
	type fileEntry struct {
		path    string
		modTime int64
	}
	seen := make(map[string]bool)
	var entries []fileEntry
	for _, m := range matches {
		if seen[m.path] {
			continue
		}
		seen[m.path] = true
		entries = append(entries, fileEntry{path: m.path, modTime: m.modTime})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].modTime > entries[j].modTime
	})

	filePaths := make([]string, len(entries))
	for i, e := range entries {
		filePaths[i] = relativizePath(e.path, workingDir)
	}

	limit := effectiveHeadLimit(params)
	sliced, truncated := applyPagination(filePaths, params.Offset, limit)

	result := strings.Join(sliced, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n(Results truncated at %d entries. Use head_limit and offset for pagination.)", limit)
	}
	return fantasy.NewTextResponse(result), nil
}

// --- Count mode ---

func grepCount(ctx context.Context, pattern, searchPath, workingDir string, params GrepParams) (fantasy.ToolResponse, error) {
	lines, err := runRgCount(ctx, pattern, searchPath, params)
	if err != nil {
		if isRgUnavailable(err) {
			return grepCountFallback(ctx, pattern, searchPath, workingDir, params)
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
	}

	if len(lines) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}

	// Relativize paths and compute totals
	var output []string
	totalMatches := 0
	for _, line := range lines {
		parts := strings.SplitN(line, ":", splitKeyValueParts)
		if len(parts) != splitKeyValueParts {
			continue
		}
		relPath := relativizePath(parts[0], workingDir)
		count := 0
		_, _ = fmt.Sscanf(parts[1], "%d", &count)
		totalMatches += count
		output = append(output, fmt.Sprintf("%s:%s", relPath, parts[1]))
	}

	limit := effectiveHeadLimit(params)
	sliced, truncated := applyPagination(output, params.Offset, limit)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matches across %d files\n\n", totalMatches, len(output))
	sb.WriteString(strings.Join(sliced, "\n"))
	if truncated {
		fmt.Fprintf(&sb, "\n\n(Showing %d of %d files. Use head_limit and offset for pagination.)", len(sliced), len(output))
	}
	return fantasy.NewTextResponse(sb.String()), nil
}

func grepCountFallback(ctx context.Context, pattern, searchPath, workingDir string, params GrepParams) (fantasy.ToolResponse, error) {
	matches, err := grepWithRegex(ctx, pattern, searchPath, params.Glob)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
	}
	if len(matches) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}

	// Count per file
	counts := make(map[string]int)
	var order []string
	for _, m := range matches {
		if counts[m.path] == 0 {
			order = append(order, m.path)
		}
		counts[m.path]++
	}

	var output []string
	totalMatches := 0
	for _, path := range order {
		c := counts[path]
		totalMatches += c
		relPath := relativizePath(path, workingDir)
		output = append(output, fmt.Sprintf("%s:%d", relPath, c))
	}

	limit := effectiveHeadLimit(params)
	sliced, truncated := applyPagination(output, params.Offset, limit)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matches across %d files\n\n", totalMatches, len(output))
	sb.WriteString(strings.Join(sliced, "\n"))
	if truncated {
		fmt.Fprintf(&sb, "\n\n(Showing %d of %d files. Use head_limit and offset for pagination.)", len(sliced), len(output))
	}
	return fantasy.NewTextResponse(sb.String()), nil
}

// --- Ripgrep command builders ---

// buildRgBaseArgs builds common ripgrep flags shared across all modes.
// Does NOT include the pattern or path — callers append those last.
func buildRgBaseArgs(params GrepParams) []string {
	var args []string

	// Include hidden files but exclude VCS dirs explicitly (matches Claude Code)
	args = append(args, "--hidden")
	for _, dir := range []string{".git", ".svn", ".hg", ".bzr", ".jj"} {
		args = append(args, "--glob", "!"+dir)
	}
	// Also exclude common noisy dirs
	for _, ex := range rgExcludeGlobs {
		args = append(args, "--glob", ex)
	}
	args = append(args, "--max-columns", "500")

	if params.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	if params.CaseInsens {
		args = append(args, "-i")
	}
	if params.Glob != "" {
		args = append(args, "--glob", params.Glob)
	}
	if params.Type != "" {
		args = append(args, "--type", params.Type)
	}

	return args
}

// appendPatternAndPath adds the search pattern and path as the final arguments.
func appendPatternAndPath(args []string, pattern, searchPath string) []string {
	if strings.HasPrefix(pattern, "-") {
		args = append(args, "-e", pattern)
	} else {
		args = append(args, "--", pattern)
	}
	args = append(args, searchPath)
	return args
}

// runRgText runs ripgrep in content mode and returns raw output lines.
func runRgText(ctx context.Context, pattern, searchPath string, params GrepParams) ([]string, error) {
	name := findRg()
	if name == "" {
		return nil, errRgUnavailable
	}

	args := buildRgBaseArgs(params)

	// Content-mode specific flags
	if showLineNumbers(params) {
		args = append(args, "-n")
	}
	before, after := resolveContext(params)
	if before > 0 && after > 0 && before == after {
		args = append(args, "-C", fmt.Sprintf("%d", before))
	} else {
		if before > 0 {
			args = append(args, "-B", fmt.Sprintf("%d", before))
		}
		if after > 0 {
			args = append(args, "-A", fmt.Sprintf("%d", after))
		}
	}

	args = appendPatternAndPath(args, pattern, searchPath)

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is resolved rg/grep binary
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return lines, nil
}

// runRgFilesList runs ripgrep with -l (files with matches).
func runRgFilesList(ctx context.Context, pattern, searchPath string, params GrepParams) ([]string, error) {
	name := findRg()
	if name == "" {
		return nil, errRgUnavailable
	}

	args := buildRgBaseArgs(params)
	args = append(args, "-l")
	args = appendPatternAndPath(args, pattern, searchPath)

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is resolved rg/grep binary
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var result []string
	for _, l := range lines {
		if l != "" {
			result = append(result, l)
		}
	}
	return result, nil
}

// runRgCount runs ripgrep with -c (count matches per file).
func runRgCount(ctx context.Context, pattern, searchPath string, params GrepParams) ([]string, error) {
	name := findRg()
	if name == "" {
		return nil, errRgUnavailable
	}

	args := buildRgBaseArgs(params)
	args = append(args, "-c")
	args = appendPatternAndPath(args, pattern, searchPath)

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is resolved rg/grep binary
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var result []string
	for _, l := range lines {
		if l != "" {
			result = append(result, l)
		}
	}
	return result, nil
}

// --- Go fallback (no ripgrep) ---

type grepMatch struct {
	path     string
	modTime  int64
	lineNum  int
	charNum  int
	lineText string
}

// compileGrepPatterns compiles the search pattern and optional include glob filter.
func compileGrepPatterns(pattern, include string) (*regexp.Regexp, *regexp.Regexp, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid regex pattern: %w", err)
	}
	var includeRe *regexp.Regexp
	if include != "" {
		includeRe, err = regexp.Compile(globToRegex(include))
		if err != nil {
			return nil, nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}
	return re, includeRe, nil
}

func grepWithRegex(ctx context.Context, pattern, rootPath, include string) ([]grepMatch, error) {
	re, includeRe, err := compileGrepPatterns(pattern, include)
	if err != nil {
		return nil, err
	}

	var matches []grepMatch

	err = filepath.WalkDir(rootPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // skip inaccessible entries
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
			if excludedDirs[d.Name()] {
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
			return nil //nolint:nilerr // skip entries with unreadable metadata
		}

		fileMatches := searchTextFile(path, re, info.ModTime().UnixNano())
		matches = append(matches, fileMatches...)
		if len(matches) >= maxGrepResults*2 {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return nil, err
	}

	return matches, nil
}

// searchTextFile checks if a file is text (no null bytes in the first 512 bytes),
// then searches it for the pattern. Returns all matches up to maxMatchesPerFile.
func searchTextFile(path string, re *regexp.Regexp, modTime int64) []grepMatch {
	f, err := os.Open(path) //nolint:gosec // path from WalkDir within working directory
	if err != nil {
		return nil
	}
	defer f.Close()

	// Check for binary content by looking for null bytes in the first 512 bytes
	header := make([]byte, binaryDetectBufSize)
	hn, err := f.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
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
	scanner.Buffer(make([]byte, 0, scannerInitBufSize), scannerMaxBufSize)
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
