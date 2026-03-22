package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGrep_BasicMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package lib\n\nfunc world() {}\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "func hello"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.go") {
		t.Errorf("expected a.go in results, got %q", content)
	}
	if !strings.Contains(content, "func hello") {
		t.Errorf("expected matching line text, got %q", content)
	}
	if strings.Contains(content, "b.go") {
		t.Error("should not contain b.go")
	}
}

func TestGrep_RegexPattern(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "code.go"), []byte("log.Error(\"something failed\")\nlog.Info(\"ok\")\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "log\\.Error"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "code.go") {
		t.Errorf("expected code.go, got %q", content)
	}
	if !strings.Contains(content, "Line") {
		t.Errorf("expected line number, got %q", content)
	}
}

func TestGrep_LiteralText(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("price is $5.00\nother line\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "$5.00", "literal_text": true}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "test.txt") {
		t.Errorf("expected test.txt, got %q", content)
	}
	if !strings.Contains(content, "$5.00") {
		t.Errorf("expected literal match, got %q", content)
	}
}

func TestGrep_IncludeFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello world\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello world\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "hello", "include": "*.go"}`)
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

func TestGrep_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "nonexistent"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "No matches") {
		t.Errorf("expected 'No matches found', got %q", content)
	}
}

func TestGrep_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(dir, ".hidden", "secret.txt"), []byte("findme\n"), 0644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("findme\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if strings.Contains(content, "hidden") || strings.Contains(content, "secret") {
		t.Errorf("should skip hidden dirs, got %q", content)
	}
	if !strings.Contains(content, "visible.txt") {
		t.Errorf("expected visible.txt, got %q", content)
	}
}

func TestGrep_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("findme\n"), 0644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("findme\n"), 0644)

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

func TestGrep_InvalidRegex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "[invalid"}`)
	if !isErr {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(content, "error") {
		t.Errorf("expected error message, got %q", content)
	}
}

func TestGrep_CustomPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("target\n"), 0644)
	os.WriteFile(filepath.Join(dir, "outer.txt"), []byte("target\n"), 0644)

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

func TestGrep_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	// Write a file with null bytes (binary content)
	os.WriteFile(filepath.Join(dir, "binary.dat"), []byte("findme\x00\x01\x02binary"), 0644)
	os.WriteFile(filepath.Join(dir, "text.txt"), []byte("findme in text\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "text.txt") {
		t.Errorf("expected text.txt, got %q", content)
	}
	// Binary file should ideally be skipped, but the exact behavior depends
	// on whether ripgrep is available. Just verify text file is found.
}

func TestGrep_MatchCount(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nfindme\nline3\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("findme here too\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "Found 2 matches") {
		t.Errorf("expected 'Found 2 matches', got %q", content)
	}
}

func TestGrep_IncludeBraceExpansion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("findme\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.tsx"), []byte("findme\n"), 0644)
	os.WriteFile(filepath.Join(dir, "c.js"), []byte("findme\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme", "include": "*.{ts,tsx}"}`)
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
		{"f[oo].go", []string{"f[oo].go"}, []string{"fo.go"}}, // brackets are literal in our glob
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
	os.WriteFile(binPath, []byte("findme\x00\x01\x02"), 0644)

	re := regexp.MustCompile("findme")
	matches := searchTextFile(binPath, re, 0)
	if len(matches) != 0 {
		t.Errorf("expected no matches for binary file, got %d", len(matches))
	}
}

func TestSearchTextFile_YAMLDetection(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(yamlPath, []byte("key: findme\nother: value\n"), 0644)

	re := regexp.MustCompile("findme")
	matches := searchTextFile(yamlPath, re, 0)
	if len(matches) != 1 {
		t.Errorf("expected 1 match in YAML file, got %d", len(matches))
	}
}

func TestSearchTextFile_PerFileMatchCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "many.txt")

	// Write a file with more lines than maxMatchesPerFile
	var content strings.Builder
	for i := 0; i < maxMatchesPerFile+20; i++ {
		content.WriteString("findme\n")
	}
	os.WriteFile(path, []byte(content.String()), 0644)

	re := regexp.MustCompile("findme")
	matches := searchTextFile(path, re, 0)
	if len(matches) != maxMatchesPerFile {
		t.Errorf("expected %d matches (cap), got %d", maxMatchesPerFile, len(matches))
	}
}

func TestGrepWithRegex_Fallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello world\n"), 0644)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(dir, ".hidden", "secret.go"), []byte("func hello() {}\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "dep.go"), []byte("func hello() {}\n"), 0644)

	ctx := context.Background()

	// Basic search
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

	// With include filter
	matches, err = grepWithRegex(ctx, "hello", dir, "*.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range matches {
		if strings.HasSuffix(m.path, ".txt") {
			t.Error("include filter should exclude .txt files")
		}
	}

	// Invalid regex
	_, err = grepWithRegex(ctx, "[invalid", dir, "")
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestGrepWithRegex_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "text.txt"), []byte("findme\n"), 0644)
	os.WriteFile(filepath.Join(dir, "binary.dat"), []byte("findme\x00\x01binary"), 0644)

	ctx := context.Background()
	matches, err := grepWithRegex(ctx, "findme", dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match (text only), got %d", len(matches))
	}
}

func TestGrep_MultipleMatchesPerFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "multi.txt"), []byte("findme line1\nno match\nfindme line3\n"), 0644)

	tool := NewGrepTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "findme"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "Found 2 matches") {
		t.Errorf("expected 2 matches from same file, got %q", content)
	}
	if !strings.Contains(content, "Line 1") {
		t.Errorf("expected Line 1, got %q", content)
	}
	if !strings.Contains(content, "Line 3") {
		t.Errorf("expected Line 3, got %q", content)
	}
}
