package agent

import (
	"context"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
)

// callTool invokes an AgentTool with JSON input and returns the response.
func callTool(t *testing.T, tool fantasy.AgentTool, input string) fantasy.ToolResponse {
	t.Helper()
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "test",
		Name:  tool.Info().Name,
		Input: input,
	})
	if err != nil {
		t.Fatalf("tool %q error: %v", tool.Info().Name, err)
	}
	return resp
}

func TestMemoryRead_Empty(t *testing.T) {
	dir := t.TempDir()
	tool := toolMemoryRead(dir)
	resp := callTool(t, tool, `{}`)
	if resp.Content != "No memories stored yet." {
		t.Errorf("expected empty message, got %q", resp.Content)
	}
}

func TestMemoryWrite_AndRead(t *testing.T) {
	dir := t.TempDir()
	writeTool := toolMemoryWrite(dir)
	readTool := toolMemoryRead(dir)

	resp := callTool(t, writeTool, `{"content": "User prefers YAML"}`)
	if resp.IsError {
		t.Fatalf("write failed: %s", resp.Content)
	}

	resp = callTool(t, readTool, `{}`)
	if resp.Content != "User prefers YAML" {
		t.Errorf("expected memory content, got %q", resp.Content)
	}
}

func TestMemoryWrite_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	tool := toolMemoryWrite(dir)
	resp := callTool(t, tool, `{"content": ""}`)
	if !resp.IsError {
		t.Error("expected error for empty content")
	}
}

func TestTodos_CreateAndUpdate(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)

	resp := callTool(t, tool, `{"todos": [
		{"content": "Set up schema", "status": "completed"},
		{"content": "Write API", "status": "in_progress", "active_form": "Writing API"},
		{"content": "Add tests", "status": "pending"}
	]}`)
	if resp.IsError {
		t.Fatalf("error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "1/3 completed") {
		t.Errorf("expected progress, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Completed: Set up schema") {
		t.Errorf("expected completed item, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Started: Write API") {
		t.Errorf("expected started item, got %q", resp.Content)
	}

	// Verify file
	todos, err := config.ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}
}

func TestTodos_ChangeTracking(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)

	callTool(t, tool, `{"todos": [
		{"content": "Task A", "status": "in_progress"},
		{"content": "Task B", "status": "pending"}
	]}`)

	resp := callTool(t, tool, `{"todos": [
		{"content": "Task A", "status": "completed"},
		{"content": "Task B", "status": "in_progress"}
	]}`)

	if !strings.Contains(resp.Content, "Completed: Task A") {
		t.Errorf("expected Task A completed, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Started: Task B") {
		t.Errorf("expected Task B started, got %q", resp.Content)
	}
}

func TestTodos_InvalidStatus(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)
	resp := callTool(t, tool, `{"todos": [{"content": "Bad", "status": "invalid"}]}`)
	if !resp.IsError {
		t.Error("expected error for invalid status")
	}
	if !strings.Contains(resp.Content, "invalid status") {
		t.Errorf("expected error about invalid status, got %q", resp.Content)
	}
}

func TestTodos_EmptyList(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)
	resp := callTool(t, tool, `{"todos": []}`)
	if resp.IsError {
		t.Fatalf("error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "0/0") {
		t.Errorf("expected 0/0, got %q", resp.Content)
	}
}

func TestBuildSystemPrompt_WithMemoryAndTodos(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "You are a helpful agent.",
		Soul:   "Be kind.",
	}

	prompt := buildSystemPrompt(cfg, "I am Agent X", "User likes Go", "- [x] Done\n- [ ] Next\n")

	for _, want := range []string{"## Identity", "## Memories", "## Current Tasks", "Be kind.", "You are a helpful agent."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q in prompt", want)
		}
	}
}

func TestBuildSystemPrompt_EmptyOptionalSections(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Instructions here.",
	}

	prompt := buildSystemPrompt(cfg, "", "", "")
	for _, absent := range []string{"## Identity", "## Memories", "## Current Tasks"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("empty section %q should not appear", absent)
		}
	}
	if !strings.Contains(prompt, "Instructions here.") {
		t.Error("missing prompt body")
	}
}
