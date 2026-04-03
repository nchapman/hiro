package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestResolveBashTimeout(t *testing.T) {
	tests := []struct {
		name      string
		timeoutMs int
		want      time.Duration
	}{
		{"zero uses auto-background", 0, autoBackgroundAfter},
		{"negative uses auto-background", -1, autoBackgroundAfter},
		{"normal value", 5000, 5 * time.Second},
		{"clamped to max", maxBashTimeout + 100, time.Duration(maxBashTimeout) * time.Millisecond},
		{"exact max", maxBashTimeout, time.Duration(maxBashTimeout) * time.Millisecond},
		{"small value", 100, 100 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveBashTimeout(tt.timeoutMs)
			if got != tt.want {
				t.Errorf("resolveBashTimeout(%d) = %v, want %v", tt.timeoutMs, got, tt.want)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, result string)
	}{
		{
			name:  "short string unchanged",
			input: "hello world",
			check: func(t *testing.T, result string) {
				if result != "hello world" {
					t.Errorf("got %q, want %q", result, "hello world")
				}
			},
		},
		{
			name:  "empty string",
			input: "",
			check: func(t *testing.T, result string) {
				if result != "" {
					t.Errorf("got %q, want empty string", result)
				}
			},
		},
		{
			name:  "exactly maxOutputLen",
			input: strings.Repeat("x", maxOutputLen),
			check: func(t *testing.T, result string) {
				if len(result) != maxOutputLen {
					t.Errorf("len = %d, want %d", len(result), maxOutputLen)
				}
			},
		},
		{
			name:  "exceeds maxOutputLen",
			input: strings.Repeat("a\n", maxOutputLen),
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "truncated") {
					t.Error("expected truncation marker")
				}
				// Should have beginning and end
				if !strings.HasPrefix(result, "a\n") {
					t.Error("expected output to start with original content")
				}
				if !strings.HasSuffix(result, "a\n") {
					t.Error("expected output to end with original content")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateOutput(tt.input)
			tt.check(t, result)
		})
	}
}

func TestFormatBashResult(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		stderr  string
		execErr error
		isErr   bool
		check   func(t *testing.T, content string)
	}{
		{
			name:   "stdout only",
			stdout: "hello",
			check: func(t *testing.T, content string) {
				if content != "hello" {
					t.Errorf("got %q, want %q", content, "hello")
				}
			},
		},
		{
			name:   "stderr only",
			stderr: "warning",
			check: func(t *testing.T, content string) {
				if !strings.Contains(content, "STDERR:") || !strings.Contains(content, "warning") {
					t.Errorf("expected STDERR with warning, got %q", content)
				}
			},
		},
		{
			name:   "stdout and stderr",
			stdout: "out",
			stderr: "err",
			check: func(t *testing.T, content string) {
				if !strings.Contains(content, "out") || !strings.Contains(content, "STDERR:") {
					t.Errorf("expected both, got %q", content)
				}
			},
		},
		{
			name:  "no output",
			check: func(t *testing.T, content string) {
				if !strings.Contains(content, "no output") {
					t.Errorf("expected '(no output)', got %q", content)
				}
			},
		},
		{
			name:    "error with no output",
			execErr: fmt.Errorf("command failed"),
			isErr:   true,
			check: func(t *testing.T, content string) {
				if !strings.Contains(content, "command failed") {
					t.Errorf("expected error message, got %q", content)
				}
			},
		},
		{
			name:    "error with output",
			stdout:  "partial output",
			execErr: fmt.Errorf("command failed"),
			isErr:   true,
			check: func(t *testing.T, content string) {
				if !strings.Contains(content, "partial output") {
					t.Errorf("expected output in error, got %q", content)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := formatBashResult(tt.stdout, tt.stderr, tt.execErr)
			if tt.isErr && !resp.IsError {
				t.Error("expected error response")
			}
			if !tt.isErr && resp.IsError {
				t.Errorf("unexpected error response: %s", resp.Content)
			}
			tt.check(t, resp.Content)
		})
	}
}

func TestBash_CustomTimeout(t *testing.T) {
	// A quick command with a very short timeout should still complete.
	tool := NewBashTool(t.TempDir(), NewBackgroundJobManager(nil))
	content, isErr := runTool(t, tool, `{"command": "echo fast", "timeout": 5000}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "fast") {
		t.Errorf("expected 'fast', got %q", content)
	}
}

func TestBash_ContextCancellation(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	tool := NewBashTool(t.TempDir(), mgr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test",
		Name:  "Bash",
		Input: `{"command": "sleep 60"}`,
	})
	if err != nil {
		t.Fatalf("tool error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error for cancelled context")
	}
	if !strings.Contains(resp.Content, "cancelled") {
		t.Errorf("expected 'cancelled' error, got %q", resp.Content)
	}
}

func TestBash_AutoBackground(t *testing.T) {
	mgr := NewBackgroundJobManager(nil)
	t.Cleanup(func() { mgr.KillAll() })
	// Use a very short timeout to trigger auto-backgrounding.
	tool := NewBashTool(t.TempDir(), mgr)
	content, isErr := runTool(t, tool, `{"command": "sleep 60", "timeout": 200}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "background") && !strings.Contains(content, "Background") {
		t.Errorf("expected auto-background message, got %q", content)
	}
}

func TestBash_EnvFn(t *testing.T) {
	mgr := NewBackgroundJobManager(func() []string {
		return []string{"TEST_SECRET=mysecret"}
	})
	tool := NewBashTool(t.TempDir(), mgr)
	content, isErr := runTool(t, tool, `{"command": "echo $TEST_SECRET"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "mysecret") {
		t.Errorf("expected secret in output, got %q", content)
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
