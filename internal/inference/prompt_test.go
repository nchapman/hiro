package inference

import (
	"strings"
	"testing"

	"github.com/nchapman/hivebot/internal/config"
)

func TestBuildSystemPrompt_MinimalConfig(t *testing.T) {
	cfg := config.AgentConfig{Prompt: "You are a helpful assistant."}
	got := buildSystemPrompt(cfg, "", "", "", nil)

	if !strings.Contains(got, "You are a helpful assistant.") {
		t.Error("expected main prompt in output")
	}
	if !strings.Contains(got, "## Security") {
		t.Error("expected security section")
	}
	// Sections that should NOT appear without data.
	for _, section := range []string{"## Identity", "## Memories", "## Current Tasks", "## Available Secrets", "## Tool Notes", "## Skills"} {
		if strings.Contains(got, section) {
			t.Errorf("unexpected section %q in minimal prompt", section)
		}
	}
}

func TestBuildSystemPrompt_AllSections(t *testing.T) {
	cfg := config.AgentConfig{
		Prompt: "Main instructions.",
		Soul:   "Be kind.",
		Tools:  "Use bash carefully.",
		Skills: []config.SkillConfig{
			{Name: "deploy", Description: "Deploy to production."},
		},
	}
	got := buildSystemPrompt(cfg, "Agent Alpha", "Remember X.", "- [ ] Do Y", []string{"API_KEY", "DB_PASS"})

	for _, want := range []string{
		"Be kind.",
		"## Identity", "Agent Alpha",
		"## Memories", "Remember X.",
		"## Current Tasks", "Do Y",
		"## Available Secrets", "`API_KEY`", "`DB_PASS`",
		"Main instructions.",
		"## Tool Notes", "Use bash carefully.",
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
		Soul:   "SOUL_CONTENT",
		Tools:  "TOOL_NOTES",
		Skills: []config.SkillConfig{{Name: "s", Description: "d"}},
	}
	got := buildSystemPrompt(cfg, "IDENTITY", "MEMORIES", "TODOS", []string{"SECRET"})

	// Verify ordering: soul < identity < memories < todos < secrets < instructions < tools < skills < security.
	order := []string{
		"SOUL_CONTENT",
		"IDENTITY",
		"MEMORIES",
		"TODOS",
		"SECRET",
		"MAIN_INSTRUCTIONS",
		"TOOL_NOTES",
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
	got := buildSystemPrompt(cfg, "", "", "", []string{})
	if strings.Contains(got, "## Available Secrets") {
		t.Error("secrets section should not appear with empty slice")
	}
}
