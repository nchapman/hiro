package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- Default mode (files_with_matches) ---

func TestGrep_FilesWithMatches_Default(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package lib\n\nfunc world() {}\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "func hello"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.go") {
		t.Errorf("expected a.go in results, got %q", content)
	}
	if strings.Contains(content, "b.go") {
		t.Error("should not contain b.go")
	}
}

func TestGrep_FilesWithMatches_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "nonexistent"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "No matches") {
		t.Errorf("expected 'No matches found', got %q", content)
	}
}

// --- Content mode ---

func TestGrep_ContentMode_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc hello() {}\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "func hello", "output_mode": "content"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.go") {
		t.Errorf("expected a.go in results, got %q", content)
	}
	if !strings.Contains(content, "func hello") {
		t.Errorf("expected matching line text, got %q", content)
	}
}

func TestGrep_ContentMode_Regex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "code.go"), []byte("log.Error(\"something failed\")\nlog.Info(\"ok\")\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "log\\.Error", "output_mode": "content"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "code.go") {
		t.Errorf("expected code.go, got %q", content)
	}
}

func TestGrep_ContentMode_LiteralText(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("price is $5.00\nother line\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "$5.00", "literal_text": true, "output_mode": "content"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "$5.00") {
		t.Errorf("expected literal match, got %q", content)
	}
}

// --- Count mode ---

func TestGrep_CountMode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("findme\nno match\nfindme\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("findme here\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme", "output_mode": "count"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "3 matches") {
		t.Errorf("expected 3 total matches, got %q", content)
	}
	if !strings.Contains(content, "2 files") {
		t.Errorf("expected 2 files, got %q", content)
	}
}

// --- Filters ---

func TestGrep_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello world\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello world\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "hello", "glob": "*.go"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.go") {
		t.Errorf("expected a.go, got %q", content)
	}
	if strings.Contains(content, "b.txt") {
		t.Error("should not contain b.txt when filtered to *.go")
	}
}

func TestGrep_GlobBraceExpansion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("findme\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.tsx"), []byte("findme\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.js"), []byte("findme\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme", "glob": "*.{ts,tsx}"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.ts") {
		t.Errorf("expected a.ts, got %q", content)
	}
	if !strings.Contains(content, "b.tsx") {
		t.Errorf("expected b.tsx, got %q", content)
	}
	if strings.Contains(content, "c.js") {
		t.Error("should not contain c.js")
	}
}

func TestGrep_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("Hello World\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "hello world", "i": true}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.txt") {
		t.Errorf("expected a.txt match with case-insensitive, got %q", content)
	}
}

// --- Exclusions ---

func TestGrep_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("findme\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("findme\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if strings.Contains(content, "node_modules") {
		t.Errorf("should skip node_modules, got %q", content)
	}
	if !strings.Contains(content, "app.js") {
		t.Errorf("expected app.js, got %q", content)
	}
}

func TestGrep_CustomPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("target\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "outer.txt"), []byte("target\n"), 0o644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "target", "path": "`+sub+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "inner.txt") {
		t.Errorf("expected inner.txt, got %q", content)
	}
	if strings.Contains(content, "outer.txt") {
		t.Error("should not contain outer.txt from parent dir")
	}
}

// --- Validation ---

func TestGrep_EmptyPattern(t *testing.T) {
	tool := NewGrepTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"pattern": ""}`)
	if !isErr {
		t.Fatal("expected error for empty pattern")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestGrep_InvalidOutputMode(t *testing.T) {
	tool := NewGrepTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"pattern": "x", "output_mode": "invalid"}`)
	if !isErr {
		t.Fatal("expected error for invalid output_mode")
	}
	if !strings.Contains(content, "invalid output_mode") {
		t.Errorf("expected output_mode error, got %q", content)
	}
}

// --- Go fallback internals ---

func TestGlobToRegex(t *testing.T) {
	tests := []struct {
		glob    string
		match   []string
		noMatch []string
	}{
		{"*.go", []string{"main.go", "test.go"}, []string{"main.goextra", "main.txt"}},
		{"*.{ts,tsx}", []string{"app.ts", "app.tsx"}, []string{"app.js", "app.tsxyz"}},
		{"test.*.js", []string{"test.min.js"}, []string{"test_min.js"}},
		{"*.min.js", []string{"app.min.js"}, []string{"app.min.jsx"}},
		{"file+name.go", []string{"file+name.go"}, []string{"filename.go"}},
		{"f[oo].go", []string{"f[oo].go"}, []string{"fo.go"}},
	}

	for _, tt := range tests {
		re, err := regexp.Compile(globToRegex(tt.glob))
		if err != nil {
			t.Errorf("globToRegex(%q) produced invalid regex: %v", tt.glob, err)
			continue
		}
		for _, s := range tt.match {
			if !re.MatchString(s) {
				t.Errorf("globToRegex(%q) should match %q but didn't", tt.glob, s)
			}
		}
		for _, s := range tt.noMatch {
			if re.MatchString(s) {
				t.Errorf("globToRegex(%q) should NOT match %q but did", tt.glob, s)
			}
		}
	}
}

func TestSearchTextFile_SkipsBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "binary.dat")
	os.WriteFile(binPath, []byte("findme\x00\x01\x02"), 0o644)

	re := regexp.MustCompile("findme")
	matches := searchTextFile(binPath, re, 0)
	if len(matches) != 0 {
		t.Errorf("expected no matches for binary file, got %d", len(matches))
	}
}

func TestSearchTextFile_PerFileMatchCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "many.txt")

	var content strings.Builder
	for range maxMatchesPerFile + 20 {
		content.WriteString("findme\n")
	}
	os.WriteFile(path, []byte(content.String()), 0o644)

	re := regexp.MustCompile("findme")
	matches := searchTextFile(path, re, 0)
	if len(matches) != maxMatchesPerFile {
		t.Errorf("expected %d matches (cap), got %d", maxMatchesPerFile, len(matches))
	}
}

func TestEffectiveHeadLimit(t *testing.T) {
	zero := 0
	five := 5

	tests := []struct {
		name string
		p    GrepParams
		want int
	}{
		{"nil uses default", GrepParams{}, defaultHeadLimit},
		{"zero means unlimited", GrepParams{HeadLimit: &zero}, 0},
		{"explicit value", GrepParams{HeadLimit: &five}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveHeadLimit(tt.p)
			if got != tt.want {
				t.Errorf("effectiveHeadLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestShowLineNumbers(t *testing.T) {
	yes := true
	no := false

	tests := []struct {
		name string
		p    GrepParams
		want bool
	}{
		{"nil defaults to true", GrepParams{}, true},
		{"explicit true", GrepParams{LineNumbers: &yes}, true},
		{"explicit false", GrepParams{LineNumbers: &no}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := showLineNumbers(tt.p)
			if got != tt.want {
				t.Errorf("showLineNumbers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyPagination(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}

	tests := []struct {
		name      string
		offset    int
		limit     int
		wantLen   int
		wantTrunc bool
	}{
		{"no offset no limit", 0, 0, 5, false},
		{"offset only", 2, 0, 3, false},
		{"limit only", 0, 3, 3, true},
		{"offset and limit", 1, 2, 2, true},
		{"offset beyond length", 10, 0, 0, false},
		{"limit larger than items", 0, 100, 5, false},
		{"offset with remaining less than limit", 3, 5, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Copy to avoid mutation.
			input := make([]string, len(items))
			copy(input, items)
			result, truncated := applyPagination(input, tt.offset, tt.limit)
			if len(result) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(result), tt.wantLen)
			}
			if truncated != tt.wantTrunc {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTrunc)
			}
		})
	}
}

func TestResolveContext(t *testing.T) {
	tests := []struct {
		name       string
		p          GrepParams
		wantBefore int
		wantAfter  int
	}{
		{"no context", GrepParams{}, 0, 0},
		{"A only", GrepParams{ContextA: 3}, 0, 3},
		{"B only", GrepParams{ContextB: 2}, 2, 0},
		{"A and B", GrepParams{ContextA: 3, ContextB: 2}, 2, 3},
		{"C overrides A and B", GrepParams{ContextA: 3, ContextB: 2, ContextC: 5}, 5, 5},
		{"Context alias overrides A and B", GrepParams{ContextA: 3, ContextB: 2, Context: 4}, 4, 4},
		{"ContextC takes precedence over Context", GrepParams{Context: 4, ContextC: 6}, 6, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := resolveContext(tt.p)
			if before != tt.wantBefore || after != tt.wantAfter {
				t.Errorf("resolveContext() = (%d, %d), want (%d, %d)", before, after, tt.wantBefore, tt.wantAfter)
			}
		})
	}
}

func TestRelativizeContentLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		searchPath string
		workingDir string
		want       string
	}{
		{
			name:       "absolute path with colon",
			line:       "/home/user/project/file.go:10:func main()",
			searchPath: "/home/user/project",
			workingDir: "/home/user/project",
			want:       "file.go:10:func main()",
		},
		{
			name:       "relative searchPath unchanged",
			line:       "file.go:10:func main()",
			searchPath: ".",
			workingDir: "/home/user/project",
			want:       "file.go:10:func main()",
		},
		{
			name:       "absolute path with dash separator",
			line:       "/home/user/project/file.go-context line",
			searchPath: "/home/user/project",
			workingDir: "/home/user/project",
			want:       "file.go-context line",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relativizeContentLine(tt.line, tt.searchPath, tt.workingDir)
			if got != tt.want {
				t.Errorf("relativizeContentLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRelativizePath(t *testing.T) {
	got := relativizePath("/home/user/project/src/main.go", "/home/user/project")
	if got != "src/main.go" {
		t.Errorf("relativizePath() = %q, want %q", got, "src/main.go")
	}
}

func TestGrep_ContentMode_HeadLimit(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := range 300 {
		content.WriteString(fmt.Sprintf("findme line %d\n", i))
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(content.String()), 0o644)

	tool := NewGrepTool(dir)
	result, isErr := runTool(t, tool, `{"pattern": "findme", "output_mode": "content", "head_limit": 5}`)
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	if !strings.Contains(result, "truncated") {
		t.Errorf("expected truncation notice, got %q", result)
	}
}

func TestGrep_ContentMode_Offset(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("aaa\nbbb\nccc\n"), 0o644)

	tool := NewGrepTool(dir)
	result, isErr := runTool(t, tool, `{"pattern": ".", "output_mode": "content", "offset": 1, "head_limit": 1}`)
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	// offset=1 skips the first match (aaa), head_limit=1 returns only the next (bbb).
	if strings.Contains(result, "aaa") {
		t.Errorf("expected offset to skip first match, got %q", result)
	}
	if !strings.Contains(result, "bbb") {
		t.Errorf("expected second match (bbb) in output, got %q", result)
	}
}

func TestGrep_Multiline(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("start\nmiddle\nend\n"), 0o644)

	tool := NewGrepTool(dir)
	result, isErr := runTool(t, tool, `{"pattern": "start.*middle", "output_mode": "content", "multiline": true}`)
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	if strings.Contains(result, "No matches") {
		t.Error("expected multiline match")
	}
}

func TestGrepWithRegex_Fallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello world\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden", "secret.go"), []byte("func hello() {}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "dep.go"), []byte("func hello() {}\n"), 0o644)

	ctx := context.Background()

	matches, err := grepWithRegex(ctx, "func hello", dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match (only a.go), got %d", len(matches))
	}
	if len(matches) > 0 && !strings.Contains(matches[0].path, "a.go") {
		t.Errorf("expected a.go, got %s", matches[0].path)
	}

	matches, err = grepWithRegex(ctx, "hello", dir, "*.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range matches {
		if strings.HasSuffix(m.path, ".txt") {
			t.Error("include filter should exclude .txt files")
		}
	}

	_, err = grepWithRegex(ctx, "[invalid", dir, "")
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

// Tests for the grep fallback functions directly (called when rg is unavailable).

func TestGrepContentFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("func hello() {}\nfunc world() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello world\n"), 0o644)

	ctx := context.Background()

	// Basic match.
	resp, err := grepContentFallback(ctx, "hello", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error response: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "hello") {
		t.Errorf("expected 'hello' in output, got %q", resp.Content)
	}

	// No matches.
	resp, err = grepContentFallback(ctx, "nonexistent", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "No matches") {
		t.Errorf("expected 'No matches', got %q", resp.Content)
	}

	// With line numbers disabled.
	no := false
	resp, err = grepContentFallback(ctx, "hello", dir, dir, GrepParams{LineNumbers: &no})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not have line numbers like "  1: "
	for _, line := range strings.Split(resp.Content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		if matched, _ := regexp.MatchString(`^\d+:`, line); matched {
			t.Errorf("expected no line numbers, but got %q", line)
		}
	}

	// Invalid regex.
	resp, err = grepContentFallback(ctx, "[invalid", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error for invalid regex")
	}
}

func TestGrepFilesWithMatchesFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("findme\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("findme too\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("nothing\n"), 0o644)

	ctx := context.Background()

	// Basic match.
	resp, err := grepFilesWithMatchesFallback(ctx, "findme", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "a.go") || !strings.Contains(resp.Content, "b.go") {
		t.Errorf("expected a.go and b.go, got %q", resp.Content)
	}
	if strings.Contains(resp.Content, "c.txt") {
		t.Error("should not contain c.txt")
	}

	// No matches.
	resp, err = grepFilesWithMatchesFallback(ctx, "nonexistent", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "No matches") {
		t.Errorf("expected 'No matches', got %q", resp.Content)
	}

	// Invalid regex.
	resp, err = grepFilesWithMatchesFallback(ctx, "[invalid", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error for invalid regex")
	}
}

func TestGrepCountFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("findme\nno match\nfindme\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("findme here\n"), 0o644)

	ctx := context.Background()

	// Basic count.
	resp, err := grepCountFallback(ctx, "findme", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "3 matches") {
		t.Errorf("expected '3 matches', got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "2 files") {
		t.Errorf("expected '2 files', got %q", resp.Content)
	}

	// No matches.
	resp, err = grepCountFallback(ctx, "nonexistent", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "No matches") {
		t.Errorf("expected 'No matches', got %q", resp.Content)
	}

	// Invalid regex.
	resp, err = grepCountFallback(ctx, "[invalid", dir, dir, GrepParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error for invalid regex")
	}
}

func TestGrepContentFallback_Pagination(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := range 300 {
		content.WriteString(fmt.Sprintf("findme line %d\n", i))
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(content.String()), 0o644)

	ctx := context.Background()
	limit := 5
	resp, err := grepContentFallback(ctx, "findme", dir, dir, GrepParams{HeadLimit: &limit})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "truncated") {
		t.Error("expected truncation notice")
	}
}

func TestGrepFilesWithMatchesFallback_Pagination(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file_%d.txt", i)), []byte("findme\n"), 0o644)
	}

	ctx := context.Background()
	limit := 3
	resp, err := grepFilesWithMatchesFallback(ctx, "findme", dir, dir, GrepParams{HeadLimit: &limit})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "truncated") {
		t.Errorf("expected truncation, got %q", resp.Content)
	}
}

func TestGrepCountFallback_Pagination(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file_%d.txt", i)), []byte("findme\n"), 0o644)
	}

	ctx := context.Background()
	limit := 3
	resp, err := grepCountFallback(ctx, "findme", dir, dir, GrepParams{HeadLimit: &limit})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "truncated") && !strings.Contains(resp.Content, "Showing") {
		t.Errorf("expected pagination/truncation info, got %q", resp.Content)
	}
}

func TestAppendPatternAndPath(t *testing.T) {
	// Pattern starting with "-" should use -e flag.
	args := appendPatternAndPath(nil, "-test", "/path")
	if args[0] != "-e" || args[1] != "-test" {
		t.Errorf("expected -e flag for pattern starting with '-', got %v", args)
	}

	// Normal pattern uses --.
	args = appendPatternAndPath(nil, "test", "/path")
	if args[0] != "--" || args[1] != "test" {
		t.Errorf("expected -- separator for normal pattern, got %v", args)
	}
}

func TestBuildRgBaseArgs(t *testing.T) {
	params := GrepParams{
		Multiline:  true,
		CaseInsens: true,
		Glob:       "*.go",
		Type:       "go",
	}
	args := buildRgBaseArgs(params)

	hasFlag := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}

	if !hasFlag("--hidden") {
		t.Error("expected --hidden")
	}
	if !hasFlag("-U") {
		t.Error("expected -U for multiline")
	}
	if !hasFlag("-i") {
		t.Error("expected -i for case insensitive")
	}
	if !hasFlag("*.go") {
		t.Error("expected *.go glob")
	}
	if !hasFlag("go") {
		t.Error("expected go type")
	}
}
