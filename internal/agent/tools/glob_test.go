package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlob_MatchesByExtension(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.go"), []byte("package c"), 0o644)

	tool := NewGlobTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "*.go"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.go") || !strings.Contains(content, "c.go") {
		t.Errorf("expected .go files, got %q", content)
	}
	if strings.Contains(content, "b.txt") {
		t.Error("should not contain b.txt")
	}
}

func TestGlob_MatchesInSubdirectories(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src", "pkg")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hi"), 0o644)

	tool := NewGlobTool(dir)
	// The Go fallback supports **/ prefix matching against basename
	content, isErr := runTool(t, tool, `{"pattern": "**/*.go"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "main.go") {
		t.Errorf("expected main.go in subdirectory, got %q", content)
	}
	if strings.Contains(content, "README") {
		t.Error("should not contain README.md")
	}
}

func TestGlob_CustomPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "outer.txt"), []byte("y"), 0o644)

	tool := NewGlobTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "*.txt", "path": "`+sub+`"}`)
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

func TestGlob_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden", "secret.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte("y"), 0o644)

	tool := NewGlobTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "*.go"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if strings.Contains(content, "secret") || strings.Contains(content, "hidden") {
		t.Errorf("should skip hidden dirs, got %q", content)
	}
	if !strings.Contains(content, "visible.go") {
		t.Errorf("expected visible.go, got %q", content)
	}
}

func TestGlob_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("y"), 0o644)

	tool := NewGlobTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "*.js"}`)
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

func TestGlob_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)

	tool := NewGlobTool(dir)
	content, isErr := runTool(t, tool, `{"pattern": "*.go"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "No files found") {
		t.Errorf("expected 'No files found', got %q", content)
	}
}

func TestGlob_EmptyPattern(t *testing.T) {
	tool := NewGlobTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"pattern": ""}`)
	if !isErr {
		t.Fatal("expected error for empty pattern")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestGlob_NonexistentPath(t *testing.T) {
	tool := NewGlobTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"pattern": "*.go", "path": "/nonexistent/path"}`)
	if !isErr {
		t.Fatal("expected error for nonexistent path")
	}
	if !strings.Contains(content, "error") {
		t.Errorf("expected error message, got %q", content)
	}
}

func TestGlob_PrefixedDoublestarPattern(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755)
	os.MkdirAll(filepath.Join(dir, "test"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "pkg", "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "test", "helper.go"), []byte("package test"), 0o644)
	os.WriteFile(filepath.Join(dir, "root.go"), []byte("package root"), 0o644)

	tool := NewGlobTool(dir)
	// src/**/*.go should only match files under src/
	content, isErr := runTool(t, tool, `{"pattern": "src/**/*.go"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "main.go") {
		t.Errorf("expected main.go under src/, got %q", content)
	}
	if strings.Contains(content, "helper.go") {
		t.Error("should not contain helper.go from test/")
	}
	if strings.Contains(content, "root.go") {
		t.Error("should not contain root.go")
	}
}

func TestGlobWalk_DirectlyTestedFallback(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "b", "deep.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "top.txt"), []byte("y"), 0o644)

	// Test the Go fallback directly
	paths, truncated, err := globWalk("**/*.go", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Error("should not be truncated")
	}
	found := false
	for _, p := range paths {
		if strings.Contains(p, "deep.go") {
			found = true
		}
		if strings.Contains(p, "top.txt") {
			t.Error("should not match .txt files")
		}
	}
	if !found {
		t.Errorf("expected deep.go, got %v", paths)
	}
}

func TestGlobWalk_BraceExpansion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.ts"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.tsx"), []byte("y"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("z"), 0o644)

	paths, _, err := globWalk("*.{ts,tsx}", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 matches, got %d: %v", len(paths), paths)
	}
	foundTS, foundTSX := false, false
	for _, p := range paths {
		if strings.HasSuffix(p, "app.ts") {
			foundTS = true
		}
		if strings.HasSuffix(p, "app.tsx") {
			foundTSX = true
		}
	}
	if !foundTS || !foundTSX {
		t.Errorf("expected app.ts and app.tsx, got %v", paths)
	}
}
