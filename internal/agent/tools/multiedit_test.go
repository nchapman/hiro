package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultiEdit_BasicEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("aaa bbb ccc"), 0644)

	tool := NewMultiEditTool(dir)
	input := multiEditInput(path, []MultiEditOperation{
		{OldString: "aaa", NewString: "AAA"},
		{OldString: "ccc", NewString: "CCC"},
	})
	content, isErr := runTool(t, tool, input)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "AAA bbb CCC" {
		t.Errorf("file = %q, want %q", string(data), "AAA bbb CCC")
	}
}

func TestMultiEdit_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("aaa bbb ccc"), 0644)

	tool := NewMultiEditTool(dir)
	input := multiEditInput(path, []MultiEditOperation{
		{OldString: "aaa", NewString: "AAA"},
		{OldString: "MISSING", NewString: "X"},
		{OldString: "ccc", NewString: "CCC"},
	})
	content, isErr := runTool(t, tool, input)
	if !isErr {
		t.Fatal("expected error response for partial failure")
	}
	if !strings.Contains(content, "2 of 3") {
		t.Errorf("expected partial success message, got %q", content)
	}

	// Successful edits should still be applied.
	data, _ := os.ReadFile(path)
	if string(data) != "AAA bbb CCC" {
		t.Errorf("file = %q, want %q", string(data), "AAA bbb CCC")
	}
}

func TestMultiEdit_AllFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("aaa bbb"), 0644)

	tool := NewMultiEditTool(dir)
	input := multiEditInput(path, []MultiEditOperation{
		{OldString: "MISSING1", NewString: "X"},
		{OldString: "MISSING2", NewString: "Y"},
	})
	_, isErr := runTool(t, tool, input)
	if !isErr {
		t.Fatal("expected error when all edits fail")
	}

	// File should be unchanged.
	data, _ := os.ReadFile(path)
	if string(data) != "aaa bbb" {
		t.Errorf("file should be unchanged, got %q", string(data))
	}
}

func TestMultiEdit_CreateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	tool := NewMultiEditTool(dir)
	input := multiEditInput(path, []MultiEditOperation{
		{OldString: "", NewString: "hello world"},
		{OldString: "world", NewString: "earth"},
	})
	content, isErr := runTool(t, tool, input)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello earth" {
		t.Errorf("file = %q, want %q", string(data), "hello earth")
	}
}

func TestMultiEdit_CreateFileAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("old"), 0644)

	tool := NewMultiEditTool(dir)
	input := multiEditInput(path, []MultiEditOperation{
		{OldString: "", NewString: "new content"},
	})
	content, isErr := runTool(t, tool, input)
	if !isErr {
		t.Fatal("expected error for existing file")
	}
	if !strings.Contains(content, "already exists") {
		t.Errorf("expected 'already exists' error, got %q", content)
	}
}

func TestMultiEdit_FileNotFound(t *testing.T) {
	tool := NewMultiEditTool(t.TempDir())
	input := multiEditInput("/nonexistent/file.txt", []MultiEditOperation{
		{OldString: "x", NewString: "y"},
	})
	content, isErr := runTool(t, tool, input)
	if !isErr {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(content, "not found") {
		t.Errorf("expected 'not found' error, got %q", content)
	}
}

func TestMultiEdit_EmptyEdits(t *testing.T) {
	tool := NewMultiEditTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"file_path": "/tmp/x.txt", "edits": []}`)
	if !isErr {
		t.Fatal("expected error for empty edits")
	}
	if !strings.Contains(content, "at least one") {
		t.Errorf("expected validation error, got %q", content)
	}
}

func TestMultiEdit_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("foo bar foo baz foo"), 0644)

	tool := NewMultiEditTool(dir)
	input := multiEditInput(path, []MultiEditOperation{
		{OldString: "foo", NewString: "qux", ReplaceAll: true},
	})
	content, isErr := runTool(t, tool, input)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "qux bar qux baz qux" {
		t.Errorf("file = %q, want %q", string(data), "qux bar qux baz qux")
	}
}

func TestMultiEdit_SequentialDependentEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("func old() { return 1 }"), 0644)

	tool := NewMultiEditTool(dir)
	// First edit changes old→new, second edit references the result of first
	input := multiEditInput(path, []MultiEditOperation{
		{OldString: "func old()", NewString: "func updated()"},
		{OldString: "func updated() { return 1 }", NewString: "func updated() { return 2 }"},
	})
	content, isErr := runTool(t, tool, input)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "func updated() { return 2 }" {
		t.Errorf("file = %q, want %q", string(data), "func updated() { return 2 }")
	}
}

func TestMultiEdit_RelativePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "rel.txt"), []byte("abc"), 0644)

	tool := NewMultiEditTool(dir)
	input := multiEditInput("rel.txt", []MultiEditOperation{
		{OldString: "abc", NewString: "xyz"},
	})
	content, isErr := runTool(t, tool, input)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "rel.txt"))
	if string(data) != "xyz" {
		t.Errorf("file = %q, want %q", string(data), "xyz")
	}
}

func multiEditInput(filePath string, edits []MultiEditOperation) string {
	type params struct {
		FilePath string               `json:"file_path"`
		Edits    []MultiEditOperation `json:"edits"`
	}
	b, _ := json.Marshal(params{FilePath: filePath, Edits: edits})
	return string(b)
}
