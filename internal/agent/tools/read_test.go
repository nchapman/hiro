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
	os.WriteFile(path, []byte("line one\nline two\nline three\n"), 0o644)

	tool := NewReadTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`"}`)
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
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o644)

	tool := NewReadTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "offset": 3}`)
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
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o644)

	tool := NewReadTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "limit": 2}`)
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
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o644)

	tool := NewReadTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "offset": 2, "limit": 2}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "2\tb") || !strings.Contains(content, "3\tc") {
		t.Errorf("expected lines 2-3, got %q", content)
	}
}

func TestReadFile_NotFound(t *testing.T) {
	tool := NewReadTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"file_path": "/nonexistent/file.txt"}`)
	if !isErr {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(content, "error reading file") {
		t.Errorf("expected file error, got %q", content)
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	tool := NewReadTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"file_path": ""}`)
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
	os.WriteFile(path, []byte("short\n"), 0o644)

	tool := NewReadTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "offset": 999}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "beyond end") {
		t.Errorf("expected 'beyond end' message, got %q", content)
	}
}

func TestReadFile_RelativePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644)

	tool := NewReadTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "hello.txt"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "hello world") {
		t.Errorf("expected 'hello world', got %q", content)
	}
}

func TestReadFile_OutputTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	// Write a file that will produce output exceeding maxFileReadLen.
	var content strings.Builder
	for range 5000 {
		content.WriteString("this is a long line to fill up the output buffer quickly\n")
	}
	os.WriteFile(path, []byte(content.String()), 0o644)

	tool := NewReadTool(dir)
	result, isErr := runTool(t, tool, `{"file_path": "`+path+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	if !strings.Contains(result, "truncated") {
		t.Error("expected truncation notice for large output")
	}
}

func TestReadFile_ConfinedRejectsOutside(t *testing.T) {
	origRoots := getAllowedRoots()
	defer SetAllowedRoots(origRoots)

	root := t.TempDir()
	SetAllowedRoots([]string{root})

	tool := NewReadTool(root)
	content, isErr := runTool(t, tool, `{"file_path": "/etc/passwd"}`)
	if !isErr {
		t.Fatal("expected error for path outside allowed roots")
	}
	if !strings.Contains(content, "access denied") {
		t.Errorf("expected 'access denied', got %q", content)
	}
}

func TestReadFile_RelativeSubdirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub", "dir")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("nested content\n"), 0o644)

	tool := NewReadTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "sub/dir/nested.txt"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "nested content") {
		t.Errorf("expected 'nested content', got %q", content)
	}
}
