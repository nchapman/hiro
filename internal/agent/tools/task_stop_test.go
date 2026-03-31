package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestJobKill_RunningJob(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "sleep 60")

	tool := NewTaskStopTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"job_id": "%s"}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "terminated") {
		t.Errorf("expected 'terminated', got %q", content)
	}

	// Should be gone now.
	_, ok := mgr.Get(job.ID)
	if ok {
		t.Error("expected job to be removed after kill")
	}
}

func TestJobKill_NotFound(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewTaskStopTool(mgr)
	content, isErr := runTool(t, tool, `{"job_id": "NOPE"}`)
	if !isErr {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(content, "not found") {
		t.Errorf("expected 'not found', got %q", content)
	}
}

func TestJobKill_MissingID(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewTaskStopTool(mgr)
	_, isErr := runTool(t, tool, `{"job_id": ""}`)
	if !isErr {
		t.Fatal("expected error for empty job_id")
	}
}

func TestJobKill_AlreadyCompleted(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "true")
	job.Wait(context.Background())

	tool := NewTaskStopTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"job_id": "%s"}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
}
