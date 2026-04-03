package inference

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

func TestBuildSystemPrompt_MinimalConfig(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "You are a helpful assistant."}
	got := buildSystemPrompt(cfg, EnvInfo{}, "", "", "", nil)

	if !strings.Contains(got, "You are a helpful assistant.") {
		t.Error("expected main prompt in output")
	}
	if !strings.Contains(got, "## Security") {
		t.Error("expected security section")
	}
	for _, section := range []string{"## Persona", "## Memories", "## Current Tasks", "## Secrets", "## Skills", "## Environment"} {
		if strings.Contains(got, section) {
			t.Errorf("unexpected section %q in minimal prompt", section)
		}
	}
}

func TestBuildSystemPrompt_AllSections(t *testing.T) {
	cfg := config.AgentConfig{
		Prompt: "Main instructions.",
		Skills: []config.SkillConfig{
			{Name: "deploy", Description: "Deploy to production."},
		},
	}
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/abc123",
		SessionDir:  "/hiro/instances/abc123/sessions/sess1",
		Mode:        config.ModePersistent,
	}
	got := buildSystemPrompt(cfg, env, "Friendly and precise.", "Remember X.", "- [ ] Do Y", []string{"API_KEY", "DB_PASS"})

	for _, want := range []string{
		"## Environment", "workspace/", "memory.md", "persona.md",
		"## Persona", "Friendly and precise.",
		"## Memories", "Remember X.",
		"## Current Tasks", "Do Y",
		"## Secrets", "`API_KEY`", "`DB_PASS`",
		"Main instructions.",
		"## Skills", "**deploy**", "Deploy to production.",
		"## Security",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in prompt", want)
		}
	}
}

func TestBuildSystemPrompt_SectionOrder(t *testing.T) {
	cfg := config.AgentConfig{
		Prompt: "MAIN_INSTRUCTIONS",
		Skills: []config.SkillConfig{{Name: "s", Description: "d"}},
	}
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/x",
		SessionDir:  "/hiro/instances/x/sessions/y",
		Mode:        config.ModePersistent,
	}
	got := buildSystemPrompt(cfg, env, "PERSONA", "MEMORIES", "TODOS", []string{"SECRET"})

	order := []string{
		"## Environment",
		"MEMORIES",
		"TODOS",
		"SECRET",
		"MAIN_INSTRUCTIONS",
		"PERSONA",
		"## Skills",
		"## Security",
	}
	lastIdx := -1
	for _, s := range order {
		idx := strings.Index(got, s)
		if idx < 0 {
			t.Fatalf("missing %q in prompt", s)
		}
		if idx <= lastIdx {
			t.Errorf("%q appeared before expected position", s)
		}
		lastIdx = idx
	}
}

func TestBuildSystemPrompt_NoSecretsSection_WhenEmpty(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "test"}
	got := buildSystemPrompt(cfg, EnvInfo{}, "", "", "", []string{})
	if strings.Contains(got, "## Secrets") {
		t.Error("secrets section should not appear with empty slice")
	}
}

// --- Delta replay tests ---

func TestReplayAnnounced_Empty(t *testing.T) {
	got := replayAnnounced("agents", nil)
	if len(got) != 0 {
		t.Fatalf("expected empty set, got %d entries", len(got))
	}
}

func TestReplayAnnounced_SingleDelta(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("text", "agents", []string{"assistant", "critic"}, nil),
	}
	got := replayAnnounced("agents", history)
	if !got["assistant"] || !got["critic"] {
		t.Errorf("expected {assistant, critic}, got %v", got)
	}
}

func TestReplayAnnounced_AddThenRemove(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("initial", "agents", []string{"a", "b", "c"}, nil),
		buildDeltaMessage("remove b", "agents", nil, []string{"b"}),
	}
	got := replayAnnounced("agents", history)
	if !got["a"] || !got["c"] || got["b"] {
		t.Errorf("expected {a, c}, got %v", got)
	}
}

func TestReplayAnnounced_RemoveThenReAdd(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("initial", "agents", []string{"a", "b"}, nil),
		buildDeltaMessage("remove b", "agents", nil, []string{"b"}),
		buildDeltaMessage("add b back", "agents", []string{"b"}, nil),
	}
	got := replayAnnounced("agents", history)
	if !got["a"] || !got["b"] {
		t.Errorf("expected {a, b}, got %v", got)
	}
}

func TestReplayAnnounced_FiltersByType(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("agents", "agents", []string{"assistant"}, nil),
		buildDeltaMessage("nodes", "nodes", []string{"node1"}, nil),
	}
	agents := replayAnnounced("agents", history)
	if !agents["assistant"] || agents["node1"] {
		t.Errorf("expected only assistant, got %v", agents)
	}
	nodes := replayAnnounced("nodes", history)
	if !nodes["node1"] || nodes["assistant"] {
		t.Errorf("expected only node1, got %v", nodes)
	}
}

func TestReplayAnnounced_SkipsNonDeltaMessages(t *testing.T) {
	history := []fantasy.Message{
		fantasy.NewUserMessage("hello"),
		buildDeltaMessage("agents", "agents", []string{"assistant"}, nil),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "hi there"}}},
	}
	got := replayAnnounced("agents", history)
	if len(got) != 1 || !got["assistant"] {
		t.Errorf("expected {assistant}, got %v", got)
	}
}

func TestComputeDeltas_NilProviders(t *testing.T) {
	got := computeDeltas(nil, nil, nil)
	if len(got) != 0 {
		t.Fatalf("expected no deltas, got %d", len(got))
	}
}

func TestComputeDeltas_Dedup(t *testing.T) {
	p1 := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult {
		return &DeltaResult{Message: buildDeltaMessage("first", "agents", []string{"a"}, nil)}
	}
	p2 := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult {
		return &DeltaResult{Message: buildDeltaMessage("second", "agents", []string{"b"}, nil)}
	}
	got := computeDeltas([]ContextProvider{p1, p2}, nil, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 delta (deduped), got %d", len(got))
	}
	// First provider should win.
	replay := extractDeltaReplay(got[0])
	if replay == nil || len(replay.AddedNames) == 0 || replay.AddedNames[0] != "a" {
		t.Error("expected first provider's message to win dedup")
	}
}

func TestDeltaReplay_JSONRoundTrip(t *testing.T) {
	msg := buildDeltaMessage("test", "agents", []string{"assistant"}, []string{"old"})

	// Marshal to JSON (simulates DB storage).
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Unmarshal back (simulates DB retrieval).
	var restored fantasy.Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Extract DeltaReplay from restored message.
	dr := extractDeltaReplay(restored)
	if dr == nil {
		t.Fatal("expected DeltaReplay in restored message")
	}
	if dr.ContextType != "agents" {
		t.Errorf("expected context_type 'agents', got %q", dr.ContextType)
	}
	if len(dr.AddedNames) != 1 || dr.AddedNames[0] != "assistant" {
		t.Errorf("expected added [assistant], got %v", dr.AddedNames)
	}
	if len(dr.RemovedNames) != 1 || dr.RemovedNames[0] != "old" {
		t.Errorf("expected removed [old], got %v", dr.RemovedNames)
	}
}

// --- Tool wrapper tests ---

func TestFantasyTools(t *testing.T) {
	a := fantasy.NewAgentTool("A", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	b := fantasy.NewAgentTool("B", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	tools := []Tool{wrap(a), wrap(b)}
	ft := fantasyTools(tools)
	if len(ft) != 2 {
		t.Fatalf("expected 2 fantasy tools, got %d", len(ft))
	}
	if ft[0].Info().Name != "A" || ft[1].Info().Name != "B" {
		t.Error("fantasy tools should preserve order and identity")
	}
}

func TestWrapAndWrapAll(t *testing.T) {
	a := fantasy.NewAgentTool("A", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	tool := wrap(a)
	if tool.Info().Name != "A" {
		t.Error("wrap should preserve tool identity")
	}

	b := fantasy.NewAgentTool("B", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	tools := wrapAll([]fantasy.AgentTool{a, b})
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

// --- Environment section tests ---

func TestBuildEnvironmentSection_Persistent(t *testing.T) {
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/abc-123",
		SessionDir:  "/hiro/instances/abc-123/sessions/sess-456",
		Mode:        config.ModePersistent,
	}
	got := buildEnvironmentSection(env)

	for _, want := range []string{
		"workspace/", "agents/", "memory.md", "persona.md",
		"todos.yaml", "scratch/", "tmp/",
		"/hiro/instances/abc-123",
		"/hiro/instances/abc-123/sessions/sess-456",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in environment section", want)
		}
	}
}

func TestComputeDeltas_ProviderReturnsNil(t *testing.T) {
	p := func(_ map[string]bool, _ []fantasy.Message) *DeltaResult { return nil }
	got := computeDeltas([]ContextProvider{p}, nil, nil)
	if len(got) != 0 {
		t.Fatalf("expected no deltas when provider returns nil, got %d", len(got))
	}
}

func TestExtractDeltaReplay_NoProviderOptions(t *testing.T) {
	msg := fantasy.NewUserMessage("plain message")
	if dr := extractDeltaReplay(msg); dr != nil {
		t.Error("expected nil for message without ProviderOptions")
	}
}

func TestExtractDeltaReplay_WrongKey(t *testing.T) {
	msg := fantasy.NewUserMessage("test")
	// ProviderOptions with a different key.
	msg.ProviderOptions = fantasy.ProviderOptions{}
	if dr := extractDeltaReplay(msg); dr != nil {
		t.Error("expected nil for message with no delta key")
	}
}

func TestBuildDeltaMessage_Structure(t *testing.T) {
	msg := buildDeltaMessage("hello world", "agents", []string{"b", "a"}, []string{"d", "c"})

	// Content should be wrapped in system-reminder.
	text := msg.Content[0].(fantasy.TextPart).Text
	if !strings.Contains(text, "<system-reminder>") || !strings.Contains(text, "hello world") {
		t.Error("expected system-reminder wrapped content")
	}

	// ProviderOptions should contain sorted replay data.
	dr := extractDeltaReplay(msg)
	if dr == nil {
		t.Fatal("expected DeltaReplay in ProviderOptions")
	}
	if dr.ContextType != "agents" {
		t.Errorf("expected context_type 'agents', got %q", dr.ContextType)
	}
	// Verify sorted.
	if dr.AddedNames[0] != "a" || dr.AddedNames[1] != "b" {
		t.Errorf("expected sorted added [a, b], got %v", dr.AddedNames)
	}
	if dr.RemovedNames[0] != "c" || dr.RemovedNames[1] != "d" {
		t.Errorf("expected sorted removed [c, d], got %v", dr.RemovedNames)
	}
}

func TestReplayAnnounced_DuplicateAdd(t *testing.T) {
	history := []fantasy.Message{
		buildDeltaMessage("first", "agents", []string{"a"}, nil),
		buildDeltaMessage("again", "agents", []string{"a"}, nil),
	}
	got := replayAnnounced("agents", history)
	if len(got) != 1 || !got["a"] {
		t.Errorf("duplicate add should be idempotent, got %v", got)
	}
}

func TestBuildEnvironmentSection_Ephemeral(t *testing.T) {
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/eph-1",
		SessionDir:  "/hiro/instances/eph-1/sessions/s1",
		Mode:        config.ModeEphemeral,
	}
	got := buildEnvironmentSection(env)

	if strings.Contains(got, "memory.md") {
		t.Error("ephemeral agents should not see memory.md")
	}
	if !strings.Contains(got, "scratch/") {
		t.Error("expected scratch/ in ephemeral env")
	}
	if strings.Contains(got, "Your instance directory") {
		t.Error("ephemeral agents should not get instance directory callout")
	}
	if !strings.Contains(got, "Your session directory") {
		t.Error("ephemeral agents should get session directory callout")
	}
}
