package inference

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/toolrules"

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
	proxies := buildProxyTools("/tmp", exec, allowed, nil, nil, nil, testLogger)

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
	proxies := buildProxyTools("/tmp", exec, nil, nil, nil, nil, testLogger)

	if len(proxies) != len(RemoteTools) {
		t.Errorf("nil allowlist should include all %d remote tools, got %d", len(RemoteTools), len(proxies))
	}
}

// --- Tool rule enforcement tests ---

func mustParseRules(t *testing.T, ss ...string) []toolrules.Rule {
	t.Helper()
	rules, err := toolrules.ParseRules(ss)
	if err != nil {
		t.Fatalf("ParseRules(%v): %v", ss, err)
	}
	return rules
}

func TestProxyTool_DenyRule_BlocksCall(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:      fantasy.ToolInfo{Name: "Bash"},
		executor:  exec,
		logger:    testLogger,
		denyRules: mustParseRules(t, "Bash(rm *)"),
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Bash",
		Input: `{"command":"rm -rf /"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response for denied call")
	}
	if !strings.Contains(resp.Content, "denied") {
		t.Errorf("expected denial message, got %q", resp.Content)
	}
	// Executor should NOT have been called.
	if exec.lastName != "" {
		t.Error("executor should not have been called for denied tool call")
	}
}

func TestProxyTool_DenyRule_AllowsNonMatching(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:      fantasy.ToolInfo{Name: "Bash"},
		executor:  exec,
		logger:    testLogger,
		denyRules: mustParseRules(t, "Bash(rm *)"),
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Bash",
		Input: `{"command":"curl https://example.com"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("expected success, got error: %s", resp.Content)
	}
}

func TestProxyTool_AllowLayer_BlocksNonMatching(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:        fantasy.ToolInfo{Name: "Bash"},
		executor:    exec,
		logger:      testLogger,
		allowLayers: [][]toolrules.Rule{mustParseRules(t, "Bash(curl *)")},
	}

	// Non-matching command should be denied.
	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Bash",
		Input: `{"command":"rm -rf /"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response for call not matching allow rules")
	}
}

func TestProxyTool_AllowLayer_AllowsMatching(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:        fantasy.ToolInfo{Name: "Bash"},
		executor:    exec,
		logger:      testLogger,
		allowLayers: [][]toolrules.Rule{mustParseRules(t, "Bash(curl *)")},
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Bash",
		Input: `{"command":"curl https://example.com"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("expected success, got error: %s", resp.Content)
	}
}

func TestProxyTool_MultiLayerIntersection(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	// Agent allows curl and git, CP allows only curl.
	// Intersection: only curl should work.
	agentLayer := mustParseRules(t, "Bash(curl *)", "Bash(git *)")
	cpLayer := mustParseRules(t, "Bash(curl *)")
	pt := &proxyTool{
		info:        fantasy.ToolInfo{Name: "Bash"},
		executor:    exec,
		logger:      testLogger,
		allowLayers: [][]toolrules.Rule{agentLayer, cpLayer},
	}

	// curl should be allowed (matches both layers).
	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Bash",
		Input: `{"command":"curl https://example.com"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("curl should be allowed, got: %s", resp.Content)
	}

	// git should be denied (agent allows, CP doesn't).
	exec.lastName = "" // reset
	resp, err = pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-2",
		Name:  "Bash",
		Input: `{"command":"git status"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("git should be denied by CP layer")
	}
}

func TestProxyTool_DenyOverridesAllow(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:        fantasy.ToolInfo{Name: "Bash"},
		executor:    exec,
		logger:      testLogger,
		allowLayers: [][]toolrules.Rule{mustParseRules(t, "Bash(git *)")},
		denyRules:   mustParseRules(t, "Bash(git push *)"),
	}

	// git status should be allowed.
	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Bash",
		Input: `{"command":"git status"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("git status should be allowed, got: %s", resp.Content)
	}

	// git push should be denied (deny overrides allow).
	resp, err = pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-2",
		Name:  "Bash",
		Input: `{"command":"git push origin main"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("git push should be denied")
	}
}

func TestProxyTool_NoRules_PassesThrough(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:     fantasy.ToolInfo{Name: "Bash"},
		executor: exec,
		logger:   testLogger,
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Bash",
		Input: `{"command":"anything"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("no rules should mean no restriction, got: %s", resp.Content)
	}
}

func TestProxyTool_ReadDenyPath(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "file contents"}}
	pt := &proxyTool{
		info:      fantasy.ToolInfo{Name: "Read"},
		executor:  exec,
		logger:    testLogger,
		denyRules: mustParseRules(t, "Read(/etc/*)"),
	}

	// Allowed path.
	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Read",
		Input: `{"file_path":"/src/main.go"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("/src/main.go should be allowed, got: %s", resp.Content)
	}

	// Denied path.
	resp, err = pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-2",
		Name:  "Read",
		Input: `{"file_path":"/etc/passwd"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("/etc/passwd should be denied")
	}
}

func TestProxyTool_UnmatchedTool_PassesThrough(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	// Only Bash rules — Read should pass through unaffected.
	pt := &proxyTool{
		info:        fantasy.ToolInfo{Name: "Read"},
		executor:    exec,
		logger:      testLogger,
		allowLayers: [][]toolrules.Rule{mustParseRules(t, "Bash(curl *)")},
	}

	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Read",
		Input: `{"file_path":"/src/main.go"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("Read should pass through when layer only has Bash rules, got: %s", resp.Content)
	}
}

// --- ProviderOptions getter/setter ---

func TestProxyTool_ProviderOptions(t *testing.T) {
	pt := &proxyTool{
		info:     fantasy.ToolInfo{Name: "Read"},
		executor: &fakeExecutor{},
		logger:   testLogger,
	}

	// Default is nil.
	opts := pt.ProviderOptions()
	if opts != nil {
		t.Error("expected nil ProviderOptions by default")
	}

	// Set and retrieve.
	newOpts := make(fantasy.ProviderOptions)
	pt.SetProviderOptions(newOpts)
	opts = pt.ProviderOptions()
	if opts == nil {
		t.Fatal("expected non-nil ProviderOptions after set")
	}

	// Set back to nil.
	pt.SetProviderOptions(nil)
	if pt.ProviderOptions() != nil {
		t.Error("expected nil after resetting to nil")
	}
}

// --- Skill tool expansion tests ---

// newTestLoop creates a minimal Loop for testing expandToolsForSkill and UpdateToolRules.
func newTestLoop(t *testing.T, allowedTools map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule) *Loop {
	t.Helper()
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	redactor := NewRedactor(nil)
	proxyTools := buildProxyTools("/tmp", exec, allowedTools, allowLayers, denyRules, redactor, testLogger)
	l := &Loop{
		workingDir:      "/tmp",
		executor:        exec,
		redactor:        redactor,
		baseDenyRules:   denyRules,
		baseAllowLayers: allowLayers,
		tools:           wrapAll(proxyTools),
		logger:          testLogger,
	}
	// Create a minimal agent so UpdateToolRules can recreate it.
	l.agent = fantasy.NewAgent(nil, fantasy.WithTools(proxyTools...))
	return l
}

func toolNames(l *Loop) map[string]bool {
	names := make(map[string]bool)
	for _, t := range l.tools {
		names[t.Info().Name] = true
	}
	return names
}

func TestExpandToolsForSkill_AddsNewTools(t *testing.T) {
	// Agent starts with only Read.
	l := newTestLoop(t, map[string]bool{"Read": true}, nil, nil)
	if toolNames(l)["Bash"] {
		t.Fatal("Bash should not be available initially")
	}

	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "deploy",
		AllowedTools: []string{"Bash"},
	})
	if err != nil {
		t.Fatal(err)
	}

	names := toolNames(l)
	if !names["Bash"] {
		t.Error("Bash should be available after skill expansion")
	}
	if !names["Read"] {
		t.Error("Read should still be available")
	}
	if !l.skillExpanded {
		t.Error("skillExpanded should be true")
	}
}

func TestExpandToolsForSkill_SkipsAlreadyAvailable(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Bash": true, "Read": true}, nil, nil)
	initialCount := len(l.tools)

	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "test",
		AllowedTools: []string{"Bash"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// No new tools added.
	if len(l.tools) != initialCount {
		t.Errorf("expected %d tools (unchanged), got %d", initialCount, len(l.tools))
	}
	if l.skillExpanded {
		t.Error("skillExpanded should be false when no new tools added")
	}
}

func TestExpandToolsForSkill_SkipsDeniedTools(t *testing.T) {
	deny := mustParseRules(t, "Bash")
	l := newTestLoop(t, map[string]bool{"Read": true}, nil, deny)

	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "test",
		AllowedTools: []string{"Bash"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if toolNames(l)["Bash"] {
		t.Error("Bash should not be added when wholly denied")
	}
}

func TestExpandToolsForSkill_ParameterizedRulesEnforced(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Read": true}, nil, nil)

	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "deploy",
		AllowedTools: []string{"Bash(kubectl *)"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Bash should be available.
	names := toolNames(l)
	if !names["Bash"] {
		t.Fatal("Bash should be available after skill expansion")
	}

	// Find the Bash proxy tool and test that rules are enforced.
	for _, tool := range l.tools {
		if tool.Info().Name == "Bash" {
			pt := tool.AgentTool.(*proxyTool)
			// kubectl should be allowed.
			resp, err := pt.Run(context.Background(), fantasy.ToolCall{
				ID: "call-1", Name: "Bash",
				Input: `{"command":"kubectl get pods"}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if resp.IsError {
				t.Errorf("kubectl should be allowed, got: %s", resp.Content)
			}

			// rm should be denied.
			resp, err = pt.Run(context.Background(), fantasy.ToolCall{
				ID: "call-2", Name: "Bash",
				Input: `{"command":"rm -rf /"}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !resp.IsError {
				t.Error("rm should be denied by parameterized allow rule")
			}
			return
		}
	}
	t.Fatal("Bash proxy tool not found")
}

func TestExpandToolsForSkill_MultipleSkillsAccumulate(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Read": true}, nil, nil)

	// First skill grants Bash.
	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "skill-a",
		AllowedTools: []string{"Bash(kubectl *)"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second skill grants Write and additional Bash pattern.
	err = l.expandToolsForSkill(&config.SkillConfig{
		Name:         "skill-b",
		AllowedTools: []string{"Write", "Bash(helm *)"},
	})
	if err != nil {
		t.Fatal(err)
	}

	names := toolNames(l)
	if !names["Read"] || !names["Bash"] || !names["Write"] {
		t.Errorf("expected Read, Bash, Write; got %v", names)
	}

	// Skill allow layer should have accumulated rules from both skills.
	if len(l.skillAllowLayer) != 3 {
		t.Errorf("expected 3 accumulated skill rules, got %d", len(l.skillAllowLayer))
	}
}

func TestExpandToolsForSkill_EmptyAllowedTools(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Read": true}, nil, nil)

	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "info-only",
		AllowedTools: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.skillExpanded {
		t.Error("skillExpanded should be false for empty allowed_tools")
	}
}

func TestExpandToolsForSkill_InvalidRule(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Read": true}, nil, nil)

	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "bad",
		AllowedTools: []string{"Bash("},
	})
	if err == nil {
		t.Error("expected error for malformed rule")
	}
}

// --- NeedsReview / complex command tests ---

func TestProxyTool_NeedsReview_DenyWithEval(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:      fantasy.ToolInfo{Name: "Bash"},
		executor:  exec,
		logger:    testLogger,
		denyRules: mustParseRules(t, "Bash(rm *)"),
	}

	// eval makes the command uncertain — NeedsReview → denied.
	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID: "call-1", Name: "Bash",
		Input: `{"command":"eval \"rm -rf /\""}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Fatal("eval should trigger NeedsReview → denied")
	}
	if !strings.Contains(resp.Content, "complex") {
		t.Errorf("expected complexity message, got: %s", resp.Content)
	}
}

func TestProxyTool_NeedsReview_AllowWithVariableCommand(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:        fantasy.ToolInfo{Name: "Bash"},
		executor:    exec,
		logger:      testLogger,
		allowLayers: [][]toolrules.Rule{mustParseRules(t, "Bash(echo *)")},
	}

	// Variable in command position → uncertain → NeedsReview → denied.
	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID: "call-1", Name: "Bash",
		Input: `{"command":"$CMD hello"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Fatal("variable command should trigger NeedsReview → denied")
	}
}

func TestProxyTool_EmptyInput(t *testing.T) {
	exec := &fakeExecutor{result: ipc.ToolResult{Content: "ok"}}
	pt := &proxyTool{
		info:        fantasy.ToolInfo{Name: "Bash"},
		executor:    exec,
		logger:      testLogger,
		allowLayers: [][]toolrules.Rule{mustParseRules(t, "Bash(echo *)")},
	}

	// Empty input — allow layer has Bash rules, empty command won't match → denied.
	resp, err := pt.Run(context.Background(), fantasy.ToolCall{
		ID: "call-1", Name: "Bash",
		Input: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Fatal("empty command should be denied when allow rules exist")
	}
}

// --- UpdateToolRules tests ---

func TestUpdateToolRules_RebuildProxyTools(t *testing.T) {
	// Start with Bash and Read.
	l := newTestLoop(t, map[string]bool{"Bash": true, "Read": true}, nil, nil)
	if !toolNames(l)["Bash"] || !toolNames(l)["Read"] {
		t.Fatal("expected Bash and Read initially")
	}

	// Update to only Read.
	l.UpdateToolRules(
		map[string]bool{"Read": true},
		nil, nil,
	)

	names := toolNames(l)
	if names["Bash"] {
		t.Error("Bash should be removed after UpdateToolRules")
	}
	if !names["Read"] {
		t.Error("Read should remain after UpdateToolRules")
	}
}

func TestUpdateToolRules_PreservesLocalTools(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Bash": true}, nil, nil)

	// Add a fake local tool.
	localTool := fantasy.NewAgentTool("FakeLocal", "test", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	l.tools = append(l.tools, wrap(localTool))

	// Update tool rules — should preserve local tools.
	l.UpdateToolRules(
		map[string]bool{"Read": true},
		nil, nil,
	)

	names := toolNames(l)
	if !names["FakeLocal"] {
		t.Error("local tools should be preserved by UpdateToolRules")
	}
	if names["Bash"] {
		t.Error("Bash should be removed")
	}
	if !names["Read"] {
		t.Error("Read should be added")
	}
}

func TestUpdateToolRules_ResetsSkillExpansion(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Read": true}, nil, nil)

	// Expand via skill.
	err := l.expandToolsForSkill(&config.SkillConfig{
		Name:         "test",
		AllowedTools: []string{"Bash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !l.skillExpanded {
		t.Fatal("skill should be expanded")
	}
	if !toolNames(l)["Bash"] {
		t.Fatal("Bash should be available after skill expansion")
	}

	// UpdateToolRules resets skill expansion.
	l.UpdateToolRules(
		map[string]bool{"Read": true},
		nil, nil,
	)

	if l.skillExpanded {
		t.Error("skillExpanded should be reset")
	}
	if len(l.skillAllowLayer) != 0 {
		t.Error("skillAllowLayer should be cleared")
	}
	if toolNames(l)["Bash"] {
		t.Error("Bash from skill expansion should be removed after UpdateToolRules")
	}
}

func TestUpdateToolRules_WithDenyRules(t *testing.T) {
	l := newTestLoop(t, map[string]bool{"Bash": true, "Read": true}, nil, nil)

	deny := mustParseRules(t, "Bash(rm *)")
	l.UpdateToolRules(
		map[string]bool{"Bash": true, "Read": true},
		nil, deny,
	)

	// Bash should still be visible but rm should be denied at call time.
	if !toolNames(l)["Bash"] {
		t.Fatal("Bash should be visible")
	}

	// Find the Bash proxy and verify deny rules are applied.
	for _, tool := range l.tools {
		if tool.Info().Name == "Bash" {
			pt := tool.AgentTool.(*proxyTool)
			resp, err := pt.Run(context.Background(), fantasy.ToolCall{
				ID: "call-1", Name: "Bash",
				Input: `{"command":"rm -rf /"}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !resp.IsError {
				t.Error("rm should be denied after UpdateToolRules with deny rule")
			}
			return
		}
	}
	t.Fatal("Bash proxy not found")
}
