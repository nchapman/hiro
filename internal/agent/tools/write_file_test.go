package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFile_CreateNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	tool := NewWriteFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "content": "hello world"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Errorf("file content = %q, want %q", string(data), "hello world")
	}
}

func TestWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("old"), 0644)

	tool := NewWriteFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "content": "new"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("file content = %q, want %q", string(data), "new")
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "deep.txt")

	tool := NewWriteFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "content": "deep"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "deep" {
		t.Errorf("file content = %q, want %q", string(data), "deep")
	}
}

func TestWriteFile_EmptyPath(t *testing.T) {
	tool := NewWriteFileTool()
	content, isErr := runTool(t, tool, `{"path": "", "content": "x"}`)
	if !isErr {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestWriteFile_ReportsBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sized.txt")

	tool := NewWriteFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "content": "12345"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "5 bytes") {
		t.Errorf("expected byte count in response, got %q", content)
	}
}
