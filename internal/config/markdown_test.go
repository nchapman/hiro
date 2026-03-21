package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMarkdown_WithFrontmatter(t *testing.T) {
	input := `---
name: test-agent
model: claude-sonnet-4-20250514
description: A test agent
---

You are a test agent. Do test things.`

	result, err := ParseMarkdown(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := result.Frontmatter.String("name"); got != "test-agent" {
		t.Errorf("name = %q, want %q", got, "test-agent")
	}
	if got := result.Frontmatter.String("model"); got != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", got, "claude-sonnet-4-20250514")
	}
	if got := result.Frontmatter.String("description"); got != "A test agent" {
		t.Errorf("description = %q, want %q", got, "A test agent")
	}
	if got := result.Body; got != "You are a test agent. Do test things." {
		t.Errorf("body = %q, want %q", got, "You are a test agent. Do test things.")
	}
}

func TestParseMarkdown_WithoutFrontmatter(t *testing.T) {
	input := `Just some markdown content.

With multiple paragraphs.`

	result, err := ParseMarkdown(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Frontmatter) != 0 {
		t.Errorf("expected no frontmatter, got %v", result.Frontmatter)
	}
	if !strings.Contains(result.Body, "Just some markdown") {
		t.Errorf("body should contain content, got %q", result.Body)
	}
}

func TestParseMarkdown_EmptyInput(t *testing.T) {
	result, err := ParseMarkdown(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Body != "" {
		t.Errorf("expected empty body, got %q", result.Body)
	}
}

func TestParseMarkdown_UnclosedFrontmatter(t *testing.T) {
	input := `---
name: broken
`
	_, err := ParseMarkdown(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for unclosed frontmatter")
	}
}

func TestParseMarkdown_EmptyFrontmatter(t *testing.T) {
	input := `---
---

Body content here.`

	result, err := ParseMarkdown(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Body != "Body content here." {
		t.Errorf("body = %q, want %q", result.Body, "Body content here.")
	}
}

func TestFrontmatter_String_MissingKey(t *testing.T) {
	fm := Frontmatter{"name": "test"}
	if got := fm.String("missing"); got != "" {
		t.Errorf("expected empty string for missing key, got %q", got)
	}
}

func TestFrontmatter_String_WrongType(t *testing.T) {
	fm := Frontmatter{"count": 42}
	if got := fm.String("count"); got != "" {
		t.Errorf("expected empty string for non-string value, got %q", got)
	}
}

func TestLoadAgentDir(t *testing.T) {
	dir := t.TempDir()

	// Write agent.md
	agentMD := `---
name: researcher
model: claude-sonnet-4-20250514
description: A research agent
---

You are a research agent. You find information.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	// Write skills
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillMD := `---
name: search
description: Search the web for information
---

When searching, use multiple sources and cross-reference.`
	if err := os.WriteFile(filepath.Join(skillsDir, "search.md"), []byte(skillMD), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if agent.Name != "researcher" {
		t.Errorf("name = %q, want %q", agent.Name, "researcher")
	}
	if agent.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", agent.Model, "claude-sonnet-4-20250514")
	}
	if !strings.Contains(agent.Prompt, "research agent") {
		t.Errorf("prompt should contain system prompt, got %q", agent.Prompt)
	}
	if len(agent.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(agent.Skills))
	}
	if agent.Skills[0].Name != "search" {
		t.Errorf("skill name = %q, want %q", agent.Skills[0].Name, "search")
	}
}

func TestLoadAgentDir_MissingName(t *testing.T) {
	dir := t.TempDir()
	agentMD := `---
model: claude-sonnet-4-20250514
---

No name field.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAgentDir(dir)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestLoadAgentDir_NoSkillsDir(t *testing.T) {
	dir := t.TempDir()
	agentMD := `---
name: simple
---

A simple agent with no skills directory.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agent.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(agent.Skills))
	}
}

func TestLoadAgentDir_SkipsNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	agentMD := `---
name: test
---

Test agent.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write a non-.md file that should be skipped
	if err := os.WriteFile(filepath.Join(skillsDir, "notes.txt"), []byte("not a skill"), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agent.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(agent.Skills))
	}
}
