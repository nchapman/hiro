package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListFiles_BasicDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("b"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("c"), 0644)

	tool := NewListFilesTool(dir)
	content, isErr := runTool(t, tool, `{}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "a.txt") || !strings.Contains(content, "b.go") {
		t.Errorf("expected files listed, got %q", content)
	}
	if !strings.Contains(content, "sub/") {
		t.Errorf("expected directory listed with trailing slash, got %q", content)
	}
}

func TestListFiles_WithPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("x"), 0644)

	tool := NewListFilesTool(dir)
	content, isErr := runTool(t, tool, `{"path": "`+sub+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "inner.txt") {
		t.Errorf("expected inner.txt, got %q", content)
	}
}

func TestListFiles_WithPattern(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0644)
	os.WriteFile(filepath.Join(dir, "c.go"), []byte("c"), 0644)

	tool := NewListFilesTool(dir)
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

func TestListFiles_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(dir, ".hidden", "secret.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("v"), 0644)

	tool := NewListFilesTool(dir)
	content, isErr := runTool(t, tool, `{}`)
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

func TestListFiles_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("a"), 0644)

	tool := NewListFilesTool(dir)
	content, isErr := runTool(t, tool, `{}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if strings.Contains(content, "node_modules") {
		t.Errorf("should skip node_modules, got %q", content)
	}
}

func TestListFiles_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	tool := NewListFilesTool(dir)
	content, isErr := runTool(t, tool, `{}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "empty") {
		t.Errorf("expected 'empty directory' message, got %q", content)
	}
}

func TestListFiles_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("x"), 0644)

	tool := NewListFilesTool(dir)
	content, isErr := runTool(t, tool, `{"path": "`+path+`"}`)
	if !isErr {
		t.Fatal("expected error for non-directory")
	}
	if !strings.Contains(content, "not a directory") {
		t.Errorf("expected 'not a directory' error, got %q", content)
	}
}

func TestListFiles_NonexistentPath(t *testing.T) {
	tool := NewListFilesTool("/tmp")
	content, isErr := runTool(t, tool, `{"path": "/nonexistent/path"}`)
	if !isErr {
		t.Fatal("expected error for nonexistent path")
	}
	if !strings.Contains(content, "error accessing") {
		t.Errorf("expected access error, got %q", content)
	}
}
