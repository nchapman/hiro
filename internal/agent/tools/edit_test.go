package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEdit_ReplaceContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "world", "new_string": "earth"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello earth" {
		t.Errorf("file = %q, want %q", string(data), "hello earth")
	}
}

func TestEdit_DeleteContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello beautiful world"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": " beautiful", "new_string": ""}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Errorf("file = %q, want %q", string(data), "hello world")
	}
}

func TestEdit_CreateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "", "new_string": "brand new file"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "Created") {
		t.Errorf("expected 'Created' in response, got %q", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "brand new file" {
		t.Errorf("file = %q, want %q", string(data), "brand new file")
	}
}

func TestEdit_CreateFile_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("old"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "", "new_string": "new"}`)
	if !isErr {
		t.Fatal("expected error when creating existing file")
	}
	if !strings.Contains(content, "already exists") {
		t.Errorf("expected 'already exists' error, got %q", content)
	}
}

func TestEdit_OldStringNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "missing", "new_string": "x"}`)
	if !isErr {
		t.Fatal("expected error for missing old_string")
	}
	if !strings.Contains(content, "not found") {
		t.Errorf("expected 'not found' error, got %q", content)
	}
}

func TestEdit_MultipleMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("foo bar foo baz foo"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "foo", "new_string": "qux"}`)
	if !isErr {
		t.Fatal("expected error for multiple matches")
	}
	if !strings.Contains(content, "multiple times") {
		t.Errorf("expected 'multiple times' error, got %q", content)
	}
}

func TestEdit_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("foo bar foo baz foo"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "foo", "new_string": "qux", "replace_all": true}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "3 occurrence") {
		t.Errorf("expected '3 occurrence(s)' in response, got %q", content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "qux bar qux baz qux" {
		t.Errorf("file = %q, want %q", string(data), "qux bar qux baz qux")
	}
}

func TestEdit_FileNotFound(t *testing.T) {
	tool := NewEditTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"file_path": "/nonexistent/file.txt", "old_string": "x", "new_string": "y"}`)
	if !isErr {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(content, "not found") {
		t.Errorf("expected 'not found' error, got %q", content)
	}
}

func TestEdit_EmptyFilePath(t *testing.T) {
	tool := NewEditTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"file_path": "", "old_string": "x", "new_string": "y"}`)
	if !isErr {
		t.Fatal("expected error for empty file_path")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestEdit_NoChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "hello", "new_string": "hello", "replace_all": true}`)
	if !isErr {
		t.Fatal("expected error when no change")
	}
	if !strings.Contains(content, "same") {
		t.Errorf("expected 'same' error, got %q", content)
	}
}

func TestEdit_MultilineReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func foo() {\n    return 1\n}\n"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "    return 1", "new_string": "    return 2"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return 2") {
		t.Errorf("expected 'return 2' in file, got %q", string(data))
	}
}

func TestEdit_RelativePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world"), 0o644)

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "test.txt", "old_string": "world", "new_string": "earth"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "test.txt"))
	if string(data) != "hello earth" {
		t.Errorf("file = %q, want %q", string(data), "hello earth")
	}
}

func TestEdit_ConfinedRejectsOutside(t *testing.T) {
	origRoots := getAllowedRoots()
	defer SetAllowedRoots(origRoots)

	root := t.TempDir()
	SetAllowedRoots([]string{root})

	tool := NewEditTool(root)
	content, isErr := runTool(t, tool, `{"file_path": "/etc/evil.txt", "old_string": "x", "new_string": "y"}`)
	if !isErr {
		t.Fatal("expected error for path outside allowed roots")
	}
	if !strings.Contains(content, "access denied") {
		t.Errorf("expected 'access denied', got %q", content)
	}
}

func TestEdit_CreateWithParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "new.txt")

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "`+path+`", "old_string": "", "new_string": "deep content"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not found: %v", err)
	}
	if string(data) != "deep content" {
		t.Errorf("got %q, want %q", string(data), "deep content")
	}
}

func TestEdit_RelativeCreateFile(t *testing.T) {
	dir := t.TempDir()

	tool := NewEditTool(dir)
	content, isErr := runTool(t, tool, `{"file_path": "sub/new.txt", "old_string": "", "new_string": "created via relative path"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "Created") {
		t.Errorf("expected 'Created' in response, got %q", content)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	if err != nil {
		t.Fatalf("file not found at expected path: %v", err)
	}
	if string(data) != "created via relative path" {
		t.Errorf("file = %q, want %q", string(data), "created via relative path")
	}
}
