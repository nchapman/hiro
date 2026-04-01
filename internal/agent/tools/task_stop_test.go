package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestTaskStop_RunningJob(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "sleep 60")

	tool := NewTaskStopTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"task_id": "%s"}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "stopped") {
		t.Errorf("expected 'stopped', got %q", content)
	}

	// Should be gone now.
	_, ok := mgr.Get(job.ID)
	if ok {
		t.Error("expected task to be removed after stop")
	}
}

func TestTaskStop_NotFound(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewTaskStopTool(mgr)
	content, isErr := runTool(t, tool, `{"task_id": "NOPE"}`)
	if !isErr {
		t.Fatal("expected error for nonexistent task")
	}
	if !strings.Contains(content, "not found") {
		t.Errorf("expected 'not found', got %q", content)
	}
}

func TestTaskStop_MissingID(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewTaskStopTool(mgr)
	_, isErr := runTool(t, tool, `{"task_id": ""}`)
	if !isErr {
		t.Fatal("expected error for empty task_id")
	}
}

func TestTaskStop_AlreadyCompleted(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "true")
	job.Wait(context.Background())

	tool := NewTaskStopTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"task_id": "%s"}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
}
