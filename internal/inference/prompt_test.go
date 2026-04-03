package inference

import (
	"context"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

func TestBuildSystemPrompt_MinimalConfig(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "You are a helpful assistant."}
	got := buildSystemPrompt(cfg, EnvInfo{}, "", "", "", nil, nil)

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
	got := buildSystemPrompt(cfg, env, "Friendly and precise.", "Remember X.", "- [ ] Do Y", []string{"API_KEY", "DB_PASS"}, nil)

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
	got := buildSystemPrompt(cfg, env, "PERSONA", "MEMORIES", "TODOS", []string{"SECRET"}, nil)

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
	got := buildSystemPrompt(cfg, EnvInfo{}, "", "", "", []string{}, nil)
	if strings.Contains(got, "## Secrets") {
		t.Error("secrets section should not appear with empty slice")
	}
}

func TestBuildSystemPrompt_ToolContext(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "test"}
	ctx := []ToolContext{
		{Heading: "Agents", Content: "- **assistant**: General-purpose agent."},
	}
	got := buildSystemPrompt(cfg, EnvInfo{}, "", "", "", nil, ctx)

	if !strings.Contains(got, "## Agents") {
		t.Error("expected Agents section")
	}
	if !strings.Contains(got, "**assistant**") {
		t.Error("expected assistant in agent listing")
	}
	// Tool context should appear before Security.
	agentsIdx := strings.Index(got, "## Agents")
	securityIdx := strings.Index(got, "## Security")
	if agentsIdx > securityIdx {
		t.Error("Agents section should appear before Security")
	}
}

func TestCollectToolContext_Dedup(t *testing.T) {
	agentCtx := &ToolContext{Heading: "Agents", Content: "agent listing"}
	tools := []Tool{
		{AgentTool: nil, Context: agentCtx},
		{AgentTool: nil, Context: agentCtx}, // same heading+content — deduplicated
		{AgentTool: nil, Context: &ToolContext{Heading: "Agents", Content: "different content"}},
	}
	got := collectToolContext(tools)
	if len(got) != 2 {
		t.Fatalf("expected 2 context entries (same heading, different content), got %d", len(got))
	}
	if got[0].Content != "agent listing" {
		t.Error("first entry should be kept")
	}
	if got[1].Content != "different content" {
		t.Error("second entry with different content should be kept")
	}
}

func TestCollectToolContext_NilContextSkipped(t *testing.T) {
	tools := []Tool{
		{AgentTool: nil, Context: nil},
		{AgentTool: nil, Context: &ToolContext{Heading: "Nodes", Content: "node list"}},
		{AgentTool: nil, Context: nil},
	}
	got := collectToolContext(tools)
	if len(got) != 1 {
		t.Fatalf("expected 1 context entry, got %d", len(got))
	}
	if got[0].Heading != "Nodes" {
		t.Error("expected Nodes context")
	}
}

func TestCollectToolContext_Empty(t *testing.T) {
	got := collectToolContext(nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 context entries, got %d", len(got))
	}
}

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
	if tool.Context != nil {
		t.Error("wrap should produce nil context")
	}

	b := fantasy.NewAgentTool("B", "desc", func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
	tools := wrapAll([]fantasy.AgentTool{a, b})
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	for _, t2 := range tools {
		if t2.Context != nil {
			t.Error("wrapAll should produce nil context")
		}
	}
}

func TestBuildEnvironmentSection_Persistent(t *testing.T) {
	env := EnvInfo{
		WorkingDir:  "/hiro",
		InstanceDir: "/hiro/instances/abc-123",
		SessionDir:  "/hiro/instances/abc-123/sessions/sess-456",
		Mode:        config.ModePersistent,
	}
	got := buildEnvironmentSection(env)

	for _, want := range []string{
		"workspace/",
		"agents/",
		"memory.md",
		"persona.md",
		"todos.yaml",
		"scratch/",
		"tmp/",
		"/hiro/instances/abc-123",
		"/hiro/instances/abc-123/sessions/sess-456",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in environment section", want)
		}
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

	// Ephemeral agents should NOT see memory.md/persona.md
	if strings.Contains(got, "memory.md") {
		t.Error("ephemeral agents should not see memory.md")
	}
	// But should see scratch/tmp
	if !strings.Contains(got, "scratch/") {
		t.Error("expected scratch/ in ephemeral env")
	}
	// Should NOT show "Your instance directory" but SHOULD show session directory.
	if strings.Contains(got, "Your instance directory") {
		t.Error("ephemeral agents should not get instance directory callout")
	}
	if !strings.Contains(got, "Your session directory") {
		t.Error("ephemeral agents should get session directory callout")
	}
}
