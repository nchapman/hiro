package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func runTool(t *testing.T, tool fantasy.AgentTool, input string) (string, bool) {
	t.Helper()
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "test",
		Name:  tool.Info().Name,
		Input: input,
	})
	if err != nil {
		t.Fatalf("tool error: %v", err)
	}
	return resp.Content, resp.IsError
}

func TestBash_SimpleCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"command": "echo hello"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", content)
	}
}

func TestBash_WorkingDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("found it"), 0644)

	tool := NewBashTool("/tmp")
	content, isErr := runTool(t, tool, `{"command": "cat test.txt", "working_dir": "`+dir+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "found it") {
		t.Errorf("expected 'found it', got %q", content)
	}
}

func TestBash_FailingCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"command": "exit 42"}`)
	if !isErr {
		t.Fatal("expected error for failing command")
	}
	if !strings.Contains(content, "exit code 42") {
		t.Errorf("expected exit code 42 in error, got %q", content)
	}
}

func TestBash_EmptyCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"command": ""}`)
	if !isErr {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestBash_Stderr(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"command": "echo out; echo err >&2"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "out") || !strings.Contains(content, "STDERR") {
		t.Errorf("expected stdout and stderr, got %q", content)
	}
}

func TestBash_NoOutput(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	content, isErr := runTool(t, tool, `{"command": "true"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "no output") {
		t.Errorf("expected '(no output)', got %q", content)
	}
}
