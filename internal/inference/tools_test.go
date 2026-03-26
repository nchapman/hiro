package inference

import (
	"context"
	"testing"

	"github.com/nchapman/hivebot/internal/ipc"

	"charm.land/fantasy"
)

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
		info:     fantasy.ToolInfo{Name: "read_file"},
		executor: exec,
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "read_file",
		Input: `{"path":"main.go"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.lastName != "read_file" {
		t.Errorf("executor got name %q, want read_file", exec.lastName)
	}
	if exec.lastCallID != "call-1" {
		t.Errorf("executor got callID %q, want call-1", exec.lastCallID)
	}
	if exec.lastInput != `{"path":"main.go"}` {
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
		info:     fantasy.ToolInfo{Name: "read_file"},
		executor: exec,
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:   "call-2",
		Name: "read_file",
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
		info:     fantasy.ToolInfo{Name: "bash"},
		executor: exec,
		redactor: redactor,
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:   "call-3",
		Name: "bash",
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
	allowed := map[string]bool{"bash": true, "read_file": true}
	proxies := buildProxyTools("/tmp", exec, allowed, nil)

	names := make(map[string]bool)
	for _, p := range proxies {
		names[p.Info().Name] = true
	}
	if !names["bash"] || !names["read_file"] {
		t.Error("expected bash and read_file in proxies")
	}
	if names["write_file"] || names["glob"] {
		t.Error("write_file and glob should be filtered out")
	}
	if len(proxies) != 2 {
		t.Errorf("expected 2 proxies, got %d", len(proxies))
	}
}

func TestBuildProxyTools_NilAllowlist(t *testing.T) {
	exec := &fakeExecutor{}
	proxies := buildProxyTools("/tmp", exec, nil, nil)

	if len(proxies) != len(RemoteTools) {
		t.Errorf("nil allowlist should include all %d remote tools, got %d", len(RemoteTools), len(proxies))
	}
}
