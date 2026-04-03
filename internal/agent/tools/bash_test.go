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
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
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
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("found it"), 0o644)

	tool := NewBashTool("/tmp", NewBackgroundJobManager(nil))
	content, isErr := runTool(t, tool, `{"command": "cat test.txt", "working_dir": "`+dir+`"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "found it") {
		t.Errorf("expected 'found it', got %q", content)
	}
}

func TestBash_FailingCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
	content, isErr := runTool(t, tool, `{"command": "exit 42"}`)
	if !isErr {
		t.Fatal("expected error for failing command")
	}
	if !strings.Contains(content, "exit code 42") {
		t.Errorf("expected exit code 42 in error, got %q", content)
	}
}

func TestBash_EmptyCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
	content, isErr := runTool(t, tool, `{"command": ""}`)
	if !isErr {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(content, "required") {
		t.Errorf("expected 'required' error, got %q", content)
	}
}

func TestBash_Stderr(t *testing.T) {
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
	content, isErr := runTool(t, tool, `{"command": "echo out; echo err >&2"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "out") || !strings.Contains(content, "STDERR") {
		t.Errorf("expected stdout and stderr, got %q", content)
	}
}

func TestBash_NoOutput(t *testing.T) {
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
	content, isErr := runTool(t, tool, `{"command": "true"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "no output") {
		t.Errorf("expected '(no output)', got %q", content)
	}
}

func TestBash_BackgroundQuickCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
	// A fast command should complete during the 100ms fast-failure window.
	content, isErr := runTool(t, tool, `{"command": "echo bg-hello", "run_in_background": true}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	// Either returns the output directly (fast completion) or a job ID.
	if !strings.Contains(content, "bg-hello") && !strings.Contains(content, "Background job started") {
		t.Errorf("expected output or job ID, got %q", content)
	}
}

func TestBash_BackgroundLongRunning(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewBashTool(t.TempDir(), mgr)
	content, isErr := runTool(t, tool, `{"command": "sleep 60", "run_in_background": true}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "Background task started") {
		t.Fatalf("expected background task message, got %q", content)
	}
	// Clean up.
	for _, id := range getJobIDs(mgr) {
		mgr.Kill(id)
	}
}

func TestBash_BackgroundFailingCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
	content, isErr := runTool(t, tool, `{"command": "exit 99", "run_in_background": true}`)
	// Fast-failing background command should return the error inline.
	if !isErr {
		t.Fatal("expected error for failing background command")
	}
	if !strings.Contains(content, "exit code 99") {
		t.Errorf("expected exit code 99, got %q", content)
	}
}

func TestBackgroundJob_KillAll(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	for range 5 {
		_, err := mgr.Start(t.TempDir(), "sleep 60")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	}

	mgr.KillAll()

	ids := getJobIDs(mgr)
	if len(ids) != 0 {
		t.Errorf("expected 0 jobs after KillAll, got %d", len(ids))
	}
}

func TestBackgroundJob_CappedBuffer(t *testing.T) {
	cb := &cappedBuffer{}
	// Write more than maxBufferBytes.
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = 'A'
	}
	for range (maxBufferBytes / 1024) + 100 {
		cb.Write(chunk)
	}
	if len(cb.String()) > maxBufferBytes {
		t.Errorf("buffer exceeded cap: %d > %d", len(cb.String()), maxBufferBytes)
	}
	if cb.lost == 0 {
		t.Error("expected some bytes to be dropped")
	}
}

func getJobIDs(mgr *BackgroundJobManager) []string {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	ids := make([]string, 0, len(mgr.jobs))
	for id := range mgr.jobs {
		ids = append(ids, id)
	}
	return ids
}
