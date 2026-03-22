package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBackgroundJob_StartAndGetOutput(t *testing.T) {
	mgr := NewBackgroundJobManager()
	job, err := mgr.Start(t.TempDir(), "echo hello")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !job.Wait(context.Background()) {
		t.Fatal("expected job to complete")
	}

	stdout, _, done, _ := job.GetOutput()
	if !done {
		t.Fatal("expected done=true")
	}
	if !strings.Contains(stdout, "hello") {
		t.Errorf("expected stdout to contain 'hello', got %q", stdout)
	}
}

func TestBackgroundJob_Kill(t *testing.T) {
	mgr := NewBackgroundJobManager()
	job, err := mgr.Start(t.TempDir(), "sleep 60")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := mgr.Kill(job.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	_, ok := mgr.Get(job.ID)
	if ok {
		t.Error("expected job to be removed after kill")
	}
}

func TestBackgroundJob_KillNotFound(t *testing.T) {
	mgr := NewBackgroundJobManager()
	if err := mgr.Kill("999"); err == nil {
		t.Error("expected error for nonexistent job")
	}
}

func TestBackgroundJob_WaitContextCancelled(t *testing.T) {
	mgr := NewBackgroundJobManager()
	job, err := mgr.Start(t.TempDir(), "sleep 60")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Kill(job.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if job.Wait(ctx) {
		t.Error("expected Wait to return false on context cancel")
	}
}

func TestBackgroundJob_MaxJobs(t *testing.T) {
	mgr := NewBackgroundJobManager()

	var jobs []*BackgroundJob
	for i := 0; i < MaxBackgroundJobs; i++ {
		j, err := mgr.Start(t.TempDir(), "sleep 60")
		if err != nil {
			t.Fatalf("Start job %d: %v", i, err)
		}
		jobs = append(jobs, j)
	}

	_, err := mgr.Start(t.TempDir(), "echo overflow")
	if err == nil {
		t.Error("expected error when exceeding max background jobs")
	}

	for _, j := range jobs {
		mgr.Kill(j.ID)
	}
}

func TestBackgroundJob_FailingCommand(t *testing.T) {
	mgr := NewBackgroundJobManager()
	job, err := mgr.Start(t.TempDir(), "exit 42")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	job.Wait(context.Background())
	_, _, done, execErr := job.GetOutput()
	if !done {
		t.Fatal("expected done=true")
	}
	if execErr == nil {
		t.Error("expected non-nil error for failing command")
	}
}
