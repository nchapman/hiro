package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTodos_NotExists(t *testing.T) {
	todos, err := ReadTodos(t.TempDir())
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if len(todos) != 0 {
		t.Errorf("expected empty, got %d todos", len(todos))
	}
}

func TestWriteAndReadTodos(t *testing.T) {
	dir := t.TempDir()
	want := []Todo{
		{Content: "Set up schema", Status: TodoStatusCompleted},
		{Content: "Write API", Status: TodoStatusInProgress, ActiveForm: "Writing API"},
		{Content: "Add tests", Status: TodoStatusPending},
	}

	if err := WriteTodos(dir, want); err != nil {
		t.Fatalf("WriteTodos: %v", err)
	}

	got, err := ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(got))
	}
	if got[0].Status != TodoStatusCompleted || got[1].Status != TodoStatusInProgress {
		t.Errorf("statuses wrong: %v", got)
	}
	if got[1].ActiveForm != "Writing API" {
		t.Errorf("active_form wrong: %q", got[1].ActiveForm)
	}
}

func TestWriteTodos_Overwrites(t *testing.T) {
	dir := t.TempDir()

	if err := WriteTodos(dir, []Todo{{Content: "first", Status: TodoStatusPending}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteTodos(dir, []Todo{{Content: "second", Status: TodoStatusCompleted}}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if len(got) != 1 || got[0].Content != "second" {
		t.Errorf("expected overwrite, got %v", got)
	}
}

func TestWriteTodos_Permissions(t *testing.T) {
	dir := t.TempDir()
	WriteTodos(dir, []Todo{{Content: "test", Status: TodoStatusPending}})

	info, err := os.Stat(filepath.Join(dir, "todos.json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600, got %o", perm)
	}
}

func TestFormatTodos(t *testing.T) {
	todos := []Todo{
		{Content: "Done task", Status: TodoStatusCompleted},
		{Content: "Active task", Status: TodoStatusInProgress},
		{Content: "Future task", Status: TodoStatusPending},
	}

	got := FormatTodos(todos)
	if got == "" {
		t.Fatal("expected non-empty output")
	}

	expected := "- [x] Done task\n- [ ] **Active task** *(in progress)*\n- [ ] Future task\n"
	if got != expected {
		t.Errorf("got:\n%s\nwant:\n%s", got, expected)
	}
}

func TestFormatTodos_Empty(t *testing.T) {
	if got := FormatTodos(nil); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
