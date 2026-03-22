package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
)

// callTool invokes an AgentTool with JSON input and returns the response.
func callTool(t *testing.T, tool fantasy.AgentTool, input string) fantasy.ToolResponse {
	t.Helper()
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "test",
		Name:  tool.Info().Name,
		Input: input,
	})
	if err != nil {
		t.Fatalf("tool %q error: %v", tool.Info().Name, err)
	}
	return resp
}

func TestMemoryRead_Empty(t *testing.T) {
	dir := t.TempDir()
	tool := toolMemoryRead(dir)
	resp := callTool(t, tool, `{}`)
	if resp.Content != "No memories stored yet." {
		t.Errorf("expected empty message, got %q", resp.Content)
	}
}

func TestMemoryWrite_AndRead(t *testing.T) {
	dir := t.TempDir()
	writeTool := toolMemoryWrite(dir)
	readTool := toolMemoryRead(dir)

	resp := callTool(t, writeTool, `{"content": "User prefers YAML"}`)
	if resp.IsError {
		t.Fatalf("write failed: %s", resp.Content)
	}

	resp = callTool(t, readTool, `{}`)
	if resp.Content != "User prefers YAML" {
		t.Errorf("expected memory content, got %q", resp.Content)
	}
}

func TestMemoryWrite_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	tool := toolMemoryWrite(dir)
	resp := callTool(t, tool, `{"content": ""}`)
	if !resp.IsError {
		t.Error("expected error for empty content")
	}
}

func TestTodos_CreateAndUpdate(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)

	resp := callTool(t, tool, `{"todos": [
		{"content": "Set up schema", "status": "completed"},
		{"content": "Write API", "status": "in_progress", "active_form": "Writing API"},
		{"content": "Add tests", "status": "pending"}
	]}`)
	if resp.IsError {
		t.Fatalf("error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "1/3 completed") {
		t.Errorf("expected progress, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Completed: Set up schema") {
		t.Errorf("expected completed item, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Started: Write API") {
		t.Errorf("expected started item, got %q", resp.Content)
	}

	// Verify file
	todos, err := config.ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}
}

func TestTodos_ChangeTracking(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)

	callTool(t, tool, `{"todos": [
		{"content": "Task A", "status": "in_progress"},
		{"content": "Task B", "status": "pending"}
	]}`)

	resp := callTool(t, tool, `{"todos": [
		{"content": "Task A", "status": "completed"},
		{"content": "Task B", "status": "in_progress"}
	]}`)

	if !strings.Contains(resp.Content, "Completed: Task A") {
		t.Errorf("expected Task A completed, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Started: Task B") {
		t.Errorf("expected Task B started, got %q", resp.Content)
	}
}

func TestTodos_InvalidStatus(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)
	resp := callTool(t, tool, `{"todos": [{"content": "Bad", "status": "invalid"}]}`)
	if !resp.IsError {
		t.Error("expected error for invalid status")
	}
	if !strings.Contains(resp.Content, "invalid status") {
		t.Errorf("expected error about invalid status, got %q", resp.Content)
	}
}

func TestTodos_EmptyList(t *testing.T) {
	dir := t.TempDir()
	tool := toolTodos(dir)
	resp := callTool(t, tool, `{"todos": []}`)
	if resp.IsError {
		t.Fatalf("error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "0/0") {
		t.Errorf("expected 0/0, got %q", resp.Content)
	}
}

// --- use_skill tool tests ---

func TestUseSkill_ReturnsBody(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "test-skill.md")
	os.WriteFile(skillPath, []byte("---\nname: test-skill\ndescription: A test skill.\n---\n\nDo the thing step by step."), 0644)

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "test-skill", Description: "A test skill.", Path: skillPath},
		},
	}
	tool := buildSkillTool(cfg)
	resp := callTool(t, tool, `{"name": "test-skill"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Do the thing step by step") {
		t.Errorf("expected skill body, got %q", resp.Content)
	}
	// Should NOT contain frontmatter
	if strings.Contains(resp.Content, "---") {
		t.Error("response should not contain YAML frontmatter delimiters")
	}
	if strings.Contains(resp.Content, "name: test-skill") {
		t.Error("response should not contain frontmatter fields")
	}
}

func TestUseSkill_DirectorySkillListsResources(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755)
	os.MkdirAll(filepath.Join(skillDir, "references"), 0755)

	skillPath := filepath.Join(skillDir, "SKILL.md")
	os.WriteFile(skillPath, []byte("---\nname: my-skill\ndescription: Desc.\n---\n\nInstructions."), 0644)
	os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"), []byte("#!/bin/bash"), 0755)
	os.WriteFile(filepath.Join(skillDir, "references", "guide.md"), []byte("# Guide"), 0644)

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "my-skill", Description: "Desc.", Path: skillPath},
		},
	}
	tool := buildSkillTool(cfg)
	resp := callTool(t, tool, `{"name": "my-skill"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Bundled Resources") {
		t.Errorf("expected bundled resources section, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "scripts/") {
		t.Errorf("expected scripts/ listed, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "scripts/run.sh") {
		t.Errorf("expected scripts/run.sh listed, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "references/") {
		t.Errorf("expected references/ listed, got %q", resp.Content)
	}
}

func TestUseSkill_FlatSkillNoBundledResources(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)
	skillPath := filepath.Join(skillsDir, "simple.md")
	os.WriteFile(skillPath, []byte("---\nname: simple\ndescription: Simple.\n---\n\nJust do it."), 0644)

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "simple", Description: "Simple.", Path: skillPath},
		},
	}
	tool := buildSkillTool(cfg)
	resp := callTool(t, tool, `{"name": "simple"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if strings.Contains(resp.Content, "Bundled Resources") {
		t.Error("flat skill should not have bundled resources section")
	}
}

func TestUseSkill_EmptyName(t *testing.T) {
	cfg := &config.AgentConfig{}
	tool := buildSkillTool(cfg)
	resp := callTool(t, tool, `{"name": ""}`)
	if !resp.IsError {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(resp.Content, "required") {
		t.Errorf("expected 'required' error, got %q", resp.Content)
	}
}

func TestUseSkill_NotFound(t *testing.T) {
	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "search", Description: "Search."},
			{Name: "deploy", Description: "Deploy."},
		},
	}
	tool := buildSkillTool(cfg)
	resp := callTool(t, tool, `{"name": "nonexistent"}`)
	if !resp.IsError {
		t.Fatal("expected error for missing skill")
	}
	if !strings.Contains(resp.Content, "not found") {
		t.Errorf("expected 'not found' error, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "search") || !strings.Contains(resp.Content, "deploy") {
		t.Errorf("expected available skill names listed, got %q", resp.Content)
	}
}

func TestUseSkill_FileReadError(t *testing.T) {
	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "broken", Description: "Broken.", Path: "/nonexistent/skill.md"},
		},
	}
	tool := buildSkillTool(cfg)
	resp := callTool(t, tool, `{"name": "broken"}`)
	if !resp.IsError {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(resp.Content, "error reading") {
		t.Errorf("expected read error, got %q", resp.Content)
	}
}

func TestUseSkill_SeesUpdatedSkills(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "a.md")
	os.WriteFile(skillPath, []byte("skill A body"), 0644)

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "a", Description: "Skill A.", Path: skillPath},
		},
	}
	tool := buildSkillTool(cfg)

	// Mutate the config (simulating re-scan)
	skillPath2 := filepath.Join(dir, "b.md")
	os.WriteFile(skillPath2, []byte("skill B body"), 0644)
	cfg.Skills = append(cfg.Skills, config.SkillConfig{
		Name: "b", Description: "Skill B.", Path: skillPath2,
	})

	// Tool should see the new skill via the config pointer
	resp := callTool(t, tool, `{"name": "b"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "skill B body") {
		t.Errorf("expected skill B body, got %q", resp.Content)
	}
}

// --- buildSystemPrompt skill tests ---

func TestBuildSystemPrompt_WithSkills(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "You are a test agent.",
		Skills: []config.SkillConfig{
			{Name: "search", Description: "Search the web."},
			{Name: "deploy", Description: "Deploy to production."},
		},
	}
	prompt := buildSystemPrompt(cfg, "", "", "")

	if !strings.Contains(prompt, "## Skills") {
		t.Error("missing Skills section")
	}
	if !strings.Contains(prompt, "use_skill") {
		t.Error("missing use_skill instruction")
	}
	if !strings.Contains(prompt, "**search**") {
		t.Error("missing search skill listing")
	}
	if !strings.Contains(prompt, "**deploy**") {
		t.Error("missing deploy skill listing")
	}
	if !strings.Contains(prompt, "Search the web.") {
		t.Error("missing search description")
	}
	// Should NOT contain full skill body
	if strings.Contains(prompt, "full instructions") {
		t.Error("skill body should not be in prompt")
	}
}

func TestBuildSystemPrompt_NoSkills(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Instructions.",
	}
	prompt := buildSystemPrompt(cfg, "", "", "")
	if strings.Contains(prompt, "## Skills") {
		t.Error("Skills section should not appear when no skills")
	}
	if strings.Contains(prompt, "use_skill") {
		t.Error("use_skill instruction should not appear when no skills")
	}
}

func TestBuildSystemPrompt_WithMemoryAndTodos(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "You are a helpful agent.",
		Soul:   "Be kind.",
	}

	prompt := buildSystemPrompt(cfg, "I am Agent X", "User likes Go", "- [x] Done\n- [ ] Next\n")

	for _, want := range []string{"## Identity", "## Memories", "## Current Tasks", "Be kind.", "You are a helpful agent."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q in prompt", want)
		}
	}
}

func TestBuildSystemPrompt_EmptyOptionalSections(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Instructions here.",
	}

	prompt := buildSystemPrompt(cfg, "", "", "")
	for _, absent := range []string{"## Identity", "## Memories", "## Current Tasks"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("empty section %q should not appear", absent)
		}
	}
	if !strings.Contains(prompt, "Instructions here.") {
		t.Error("missing prompt body")
	}
}
