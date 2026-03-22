package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestJobOutput_CompletedJob(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "echo hello")
	job.Wait(context.Background())

	tool := NewJobOutputTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"job_id": "%s"}`, job.ID))
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

func TestJobOutput_RunningJob(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "sleep 60")
	defer mgr.Kill(job.ID)

	tool := NewJobOutputTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"job_id": "%s"}`, job.ID))
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "running") {
		t.Errorf("expected 'running' status, got %q", content)
	}
}

func TestJobOutput_NotFound(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewJobOutputTool(mgr)
	content, isErr := runTool(t, tool, `{"job_id": "NOPE"}`)
	if !isErr {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(content, "not found") {
		t.Errorf("expected 'not found', got %q", content)
	}
}

func TestJobOutput_MissingID(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewJobOutputTool(mgr)
	_, isErr := runTool(t, tool, `{"job_id": ""}`)
	if !isErr {
		t.Fatal("expected error for empty job_id")
	}
}

func TestJobOutput_FailedCommand(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, _ := mgr.Start(t.TempDir(), "echo oops >&2; exit 7")
	job.Wait(context.Background())

	tool := NewJobOutputTool(mgr)
	content, isErr := runTool(t, tool, fmt.Sprintf(`{"job_id": "%s"}`, job.ID))
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
