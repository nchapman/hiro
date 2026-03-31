package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestTaskOutput_CompletedJob(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "echo hello")
	job.Wait(context.Background())

	tool := NewTaskOutputTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"task_id": "%s"}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "completed") {
		t.Errorf("expected 'completed' status, got %q", content)
	}
	if !strings.Contains(content, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", content)
	}
}

func TestTaskOutput_RunningJob(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "sleep 60")
	defer mgr.Kill(job.ID)

	tool := NewTaskOutputTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"task_id": "%s", "block": false}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "running") {
		t.Errorf("expected 'running' status, got %q", content)
	}
}

func TestTaskOutput_NotFound(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewTaskOutputTool(mgr)
	content, isErr := runTool(t, tool, `{"task_id": "NOPE"}`)
	if !isErr {
		t.Fatal("expected error for nonexistent task")
	}
	if !strings.Contains(content, "no task found") {
		t.Errorf("expected 'no task found', got %q", content)
	}
}

func TestTaskOutput_MissingID(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewTaskOutputTool(mgr)
	_, isErr := runTool(t, tool, `{"task_id": ""}`)
	if !isErr {
		t.Fatal("expected error for empty task_id")
	}
}

func TestTaskOutput_FailedCommand(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "echo oops >&2; exit 7")
	job.Wait(context.Background())

	tool := NewTaskOutputTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"task_id": "%s"}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "Exit code 7") {
		t.Errorf("expected 'Exit code 7', got %q", content)
	}
	if !strings.Contains(content, "oops") {
		t.Errorf("expected stderr output, got %q", content)
	}
}
