package tools

import (
	"context"
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
