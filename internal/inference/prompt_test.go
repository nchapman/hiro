package inference

import (
	"strings"
	"testing"

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
