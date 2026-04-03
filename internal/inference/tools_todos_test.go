package inference

import (
	"strings"
	"testing"

	"github.com/nchapman/hiro/internal/config"
)

func TestHandleTodoWrite_Basic(t *testing.T) {
	dir := t.TempDir()
	items := []todoInput{
		{Content: "Build feature", Status: "pending"},
		{Content: "Write tests", Status: "in_progress", ActiveForm: "Writing tests"},
	}

	resp, err := handleTodoWrite(dir, items)
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "0/2 completed") {
		t.Errorf("unexpected response: %s", resp.Content)
	}

	todos, err := config.ReadTodos(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 2 {
		t.Fatalf("expected 2 todos, got %d", len(todos))
	}
	if todos[0].Content != "Build feature" || todos[0].Status != config.TodoStatusPending {
		t.Errorf("unexpected first todo: %+v", todos[0])
	}
	if todos[1].Content != "Write tests" || todos[1].Status != config.TodoStatusInProgress {
		t.Errorf("unexpected second todo: %+v", todos[1])
	}
	if todos[1].ActiveForm != "Writing tests" {
		t.Errorf("expected active_form 'Writing tests', got %q", todos[1].ActiveForm)
	}
}

func TestHandleTodoWrite_InvalidStatus(t *testing.T) {
	dir := t.TempDir()
	items := []todoInput{
		{Content: "Task", Status: "invalid_status"},
	}

	resp, err := handleTodoWrite(dir, items)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Error("expected error for invalid status")
	}
	if !strings.Contains(resp.Content, "invalid status") {
		t.Errorf("expected 'invalid status' in error, got: %s", resp.Content)
	}
}

func TestHandleTodoWrite_EmptyList(t *testing.T) {
	dir := t.TempDir()
	resp, err := handleTodoWrite(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "0/0 completed") {
		t.Errorf("unexpected response: %s", resp.Content)
	}
}

func TestHandleTodoWrite_CompletionTracking(t *testing.T) {
	dir := t.TempDir()

	// First write: two pending tasks.
	items := []todoInput{
		{Content: "Task A", Status: "pending"},
		{Content: "Task B", Status: "pending"},
	}
	_, err := handleTodoWrite(dir, items)
	if err != nil {
		t.Fatal(err)
	}

	// Second write: complete Task A, start Task B.
	items = []todoInput{
		{Content: "Task A", Status: "completed"},
		{Content: "Task B", Status: "in_progress", ActiveForm: "Working on B"},
	}
	resp, err := handleTodoWrite(dir, items)
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "1/2 completed") {
		t.Errorf("expected '1/2 completed' in response: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Completed: Task A") {
		t.Errorf("expected 'Completed: Task A' in response: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Started: Task B") {
		t.Errorf("expected 'Started: Task B' in response: %s", resp.Content)
	}
}

func TestHandleTodoWrite_AllStatuses(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		status string
		valid  bool
	}{
		{"pending", true},
		{"in_progress", true},
		{"completed", true},
		{"done", false},
		{"", false},
		{"PENDING", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			resp, err := handleTodoWrite(dir, []todoInput{
				{Content: "task", Status: tt.status},
			})
			if err != nil {
				t.Fatal(err)
			}
			if tt.valid && resp.IsError {
				t.Errorf("status %q should be valid, got error: %s", tt.status, resp.Content)
			}
			if !tt.valid && !resp.IsError {
				t.Errorf("status %q should be invalid", tt.status)
			}
		})
	}
}

func TestBuildTodoTools_ReturnsOneTool(t *testing.T) {
	dir := t.TempDir()
	tools := buildTodoTools(dir)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Info().Name != "TodoWrite" {
		t.Errorf("expected tool name 'TodoWrite', got %q", tools[0].Info().Name)
	}
}
