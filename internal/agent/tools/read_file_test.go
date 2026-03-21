package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line one\nline two\nline three\n"), 0644)

	tool := NewReadFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "1\tline one") {
		t.Errorf("expected line numbers, got %q", content)
	}
	if !strings.Contains(content, "3\tline three") {
		t.Errorf("expected line 3, got %q", content)
	}
}

func TestReadFile_WithOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0644)

	tool := NewReadFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "offset": 3}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if strings.Contains(content, "1\ta") {
		t.Error("should not contain line 1")
	}
	if !strings.Contains(content, "3\tc") {
		t.Errorf("expected to start at line 3, got %q", content)
	}
}

func TestReadFile_WithLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0644)

	tool := NewReadFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "limit": 2}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "1\ta") || !strings.Contains(content, "2\tb") {
		t.Errorf("expected lines 1-2, got %q", content)
	}
	if strings.Contains(content, "3\tc") {
		t.Error("should not contain line 3")
	}
}

func TestReadFile_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0644)

	tool := NewReadFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "offset": 2, "limit": 2}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "2\tb") || !strings.Contains(content, "3\tc") {
		t.Errorf("expected lines 2-3, got %q", content)
	}
}

func TestReadFile_NotFound(t *testing.T) {
	tool := NewReadFileTool()
	content, isErr := runTool(t, tool, `{"path": "/nonexistent/file.txt"}`)
	if !isErr {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(content, "error reading file") {
		t.Errorf("expected file error, got %q", content)
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	tool := NewReadFileTool()
	content, isErr := runTool(t, tool, `{"path": ""}`)
	if !isErr {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestReadFile_OffsetBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("short\n"), 0644)

	tool := NewReadFileTool()
	content, isErr := runTool(t, tool, `{"path": "`+path+`", "offset": 999}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "beyond end") {
		t.Errorf("expected 'beyond end' message, got %q", content)
	}
}
