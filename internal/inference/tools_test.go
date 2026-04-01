package inference

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/nchapman/hiro/internal/ipc"

	"charm.land/fantasy"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeExecutor records tool calls and returns a fixed result.
type fakeExecutor struct {
	lastCallID string
	lastName   string
	lastInput  string
	result     ipc.ToolResult
	err        error
}

func (f *fakeExecutor) ExecuteTool(_ context.Context, callID, name, input string) (ipc.ToolResult, error) {
	f.lastCallID = callID
	f.lastName = name
	f.lastInput = input
	return f.result, f.err
}

func TestProxyTool_ForwardsToExecutor(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "file contents"}}
	pt := &proxyTool{
		info:     fantasy.ToolInfo{Name: "Read"},
		executor: exec,
		logger:   testLogger,
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Read",
		Input: `{"file_path":"main.go"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.lastName != "Read" {
		t.Errorf("executor got name %q, want Read", exec.lastName)
	}
	if exec.lastCallID != "call-1" {
		t.Errorf("executor got callID %q, want call-1", exec.lastCallID)
	}
	if exec.lastInput != `{"file_path":"main.go"}` {
		t.Errorf("executor got input %q, want expected JSON", exec.lastInput)
	}

	if resp.Content != "file contents" {
		t.Errorf("got %q, want 'file contents'", resp.Content)
	}
	if resp.IsError {
		t.Error("unexpected IsError=true")
	}
}

func TestProxyTool_ErrorResult(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "not found", IsError: true}}
	pt := &proxyTool{
		info:     fantasy.ToolInfo{Name: "Read"},
		executor: exec,
		logger:   testLogger,
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:   "call-2",
		Name: "Read",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected IsError=true")
	}
	if resp.Content != "not found" {
		t.Errorf("got %q, want 'not found'", resp.Content)
	}
}

func TestProxyTool_RedactsSecrets(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "got sk-secret-12345678 in output"}}
	redactor := NewRedactor(func() []string {
		return []string{"API_KEY=sk-secret-12345678"}
	})
	pt := &proxyTool{
		info:     fantasy.ToolInfo{Name: "Bash"},
		executor: exec,
		redactor: redactor,
		logger:   testLogger,
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:   "call-3",
		Name: "Bash",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "got [API_KEY] in output" {
		t.Errorf("expected redacted output, got %q", resp.Content)
	}
}

func TestBuildProxyTools_RespectsAllowlist(t *testing.T) {
	exec := &fakeExecutor{}
	allowed := map[string]bool{"Bash": true, "Read": true}
	proxies := buildProxyTools("/tmp", exec, allowed, nil, testLogger)

	names := make(map[string]bool)
	for _, p := range proxies {
		names[p.Info().Name] = true
	}
	if !names["Bash"] || !names["Read"] {
		t.Error("expected Bash and Read in proxies")
	}
	if names["Write"] || names["Glob"] {
		t.Error("Write and Glob should be filtered out")
	}
	if len(proxies) != 2 {
		t.Errorf("expected 2 proxies, got %d", len(proxies))
	}
}

func TestBuildProxyTools_NilAllowlist(t *testing.T) {
	exec := &fakeExecutor{}
	proxies := buildProxyTools("/tmp", exec, nil, nil, testLogger)

	if len(proxies) != len(RemoteTools) {
		t.Errorf("nil allowlist should include all %d remote tools, got %d", len(RemoteTools), len(proxies))
	}
}
