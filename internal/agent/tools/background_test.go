package tools

import (
	"context"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundJob_StartAndGetOutput(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
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
	mgr := NewBackgroundJobManager(nil)
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
	mgr := NewBackgroundJobManager(nil)
	if err := mgr.Kill("999"); err == nil {
		t.Error("expected error for nonexistent job")
	}
}

func TestBackgroundJob_WaitContextCancelled(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
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
	mgr := NewBackgroundJobManager(nil)

	var jobs []*BackgroundJob
	for i := range MaxBackgroundJobs {
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
	mgr := NewBackgroundJobManager(nil)
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

func TestBackgroundJob_IDFormat(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	job, err := mgr.Start(t.TempDir(), "true")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Kill(job.ID)

	matched, _ := regexp.MatchString(`^[0-9A-F]{1,6}$`, job.ID)
	if !matched {
		t.Errorf("job ID %q does not match expected hex format", job.ID)
	}
}

func TestNotifyOnComplete_FiresForBackgroundedJob(t *testing.T) {
	var count atomic.Int32
	mgr := NewBackgroundJobManager(nil)
	mgr.OnComplete = func(job *BackgroundJob) {
		count.Add(1)
	}

	job, err := mgr.Start(t.TempDir(), "echo done")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Opt in to notification (simulates backgrounding).
	mgr.NotifyOnComplete(job.ID)
	job.Wait(context.Background())

	// Give the watcher goroutine time to fire.
	time.Sleep(50 * time.Millisecond)

	if count.Load() != 1 {
		t.Fatalf("expected 1 completion callback, got %d", count.Load())
	}
}

func TestNotifyOnComplete_DoesNotFireForSyncJob(t *testing.T) {
	called := false
	mgr := NewBackgroundJobManager(nil)
	mgr.OnComplete = func(job *BackgroundJob) {
		called = true
	}

	job, err := mgr.Start(t.TempDir(), "echo done")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Do NOT call NotifyOnComplete — simulates sync consumption.
	job.Wait(context.Background())
	mgr.Remove(job.ID)

	time.Sleep(50 * time.Millisecond)

	if called {
		t.Error("OnComplete should not fire for jobs that are consumed synchronously")
	}
}

func TestNotifyOnComplete_SuppressedByKill(t *testing.T) {
	called := false
	mgr := NewBackgroundJobManager(nil)
	mgr.OnComplete = func(job *BackgroundJob) {
		called = true
	}

	job, err := mgr.Start(t.TempDir(), "sleep 60")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Opt in, then kill — kill should suppress the notification.
	mgr.NotifyOnComplete(job.ID)
	mgr.Kill(job.ID)

	time.Sleep(50 * time.Millisecond)

	if called {
		t.Error("OnComplete should not fire when Kill() is called")
	}
}

func TestNotifyOnComplete_AtMostOnce(t *testing.T) {
	var count atomic.Int32
	mgr := NewBackgroundJobManager(nil)
	mgr.OnComplete = func(job *BackgroundJob) {
		count.Add(1)
	}

	job, err := mgr.Start(t.TempDir(), "echo done")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Call NotifyOnComplete multiple times — should still fire at most once.
	mgr.NotifyOnComplete(job.ID)
	mgr.NotifyOnComplete(job.ID)
	mgr.NotifyOnComplete(job.ID)
	job.Wait(context.Background())

	time.Sleep(50 * time.Millisecond)

	if count.Load() != 1 {
		t.Fatalf("expected exactly 1 callback, got %d", count.Load())
	}
}
