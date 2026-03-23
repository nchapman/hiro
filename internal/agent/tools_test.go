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
	tool := buildSkillTool(cfg, nil)
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
	tool := buildSkillTool(cfg, nil)
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
	tool := buildSkillTool(cfg, nil)
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
	tool := buildSkillTool(cfg, nil)
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
	tool := buildSkillTool(cfg, nil)
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
	tool := buildSkillTool(cfg, nil)
	resp := callTool(t, tool, `{"name": "broken"}`)
	if !resp.IsError {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(resp.Content, "error reading") {
		t.Errorf("expected read error, got %q", resp.Content)
	}
}

func TestUseSkill_FileReadError_WithConfinement(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	// Create a file with no read permissions to trigger a read error
	// after the confinement check passes.
	unreadablePath := filepath.Join(skillsDir, "unreadable.md")
	os.WriteFile(unreadablePath, []byte("---\nname: unreadable\n---\nBody."), 0000)

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "unreadable", Description: "Unreadable.", Path: unreadablePath},
		},
	}
	tool := buildSkillTool(cfg, []string{realPath(t, skillsDir)})
	resp := callTool(t, tool, `{"name": "unreadable"}`)
	if !resp.IsError {
		t.Fatal("expected error for unreadable file within allowed dir")
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
	tool := buildSkillTool(cfg, nil)

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

// --- path confinement tests ---

// realPath resolves symlinks in a path (e.g. macOS /var -> /private/var).
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path // not yet on disk, return as-is
	}
	return resolved
}

func TestUseSkill_PathConfinement(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	skillPath := filepath.Join(skillsDir, "ok.md")
	os.WriteFile(skillPath, []byte("---\nname: ok\ndescription: OK.\n---\n\nAllowed."), 0644)

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "ok", Description: "OK.", Path: skillPath},
		},
	}
	// Resolve symlinks on allowedDirs (mirrors production behavior in agent.go)
	tool := buildSkillTool(cfg, []string{realPath(t, skillsDir)})
	resp := callTool(t, tool, `{"name": "ok"}`)
	if resp.IsError {
		t.Fatalf("expected success for skill under allowed dir, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Allowed.") {
		t.Errorf("expected skill body, got %q", resp.Content)
	}
}

func TestUseSkill_PathConfinementRejectsOutsidePath(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()

	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	// Skill file is outside the allowed directory
	outsidePath := filepath.Join(outsideDir, "evil.md")
	os.WriteFile(outsidePath, []byte("---\nname: evil\ndescription: Evil.\n---\n\nSneaky."), 0644)

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "evil", Description: "Evil.", Path: outsidePath},
		},
	}
	tool := buildSkillTool(cfg, []string{realPath(t, skillsDir)})
	resp := callTool(t, tool, `{"name": "evil"}`)
	if !resp.IsError {
		t.Fatal("expected error for skill outside allowed directory")
	}
	if !strings.Contains(resp.Content, "outside allowed") {
		t.Errorf("expected 'outside allowed' error, got %q", resp.Content)
	}
}

func TestIsUnderAllowedDir(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		dirs    []string
		allowed bool
	}{
		{"empty dirs allows all", "/any/path", nil, true},
		{"under allowed dir", "/a/b/skill.md", []string{"/a/b"}, true},
		{"not under any dir", "/c/d/skill.md", []string{"/a/b"}, false},
		{"exact dir not allowed", "/a/b", []string{"/a/b"}, false},
		{"skip empty dir entries", "/a/b/skill.md", []string{"", "/a/b"}, true},
		{"multiple dirs, second matches", "/x/y/skill.md", []string{"/a/b", "/x/y"}, true},
		{"all empty dir entries allows all", "/any/path", []string{"", ""}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isUnderAllowedDir(tc.path, tc.dirs)
			if got != tc.allowed {
				t.Errorf("isUnderAllowedDir(%q, %v) = %v, want %v", tc.path, tc.dirs, got, tc.allowed)
			}
		})
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
	prompt := buildSystemPrompt(cfg, "", "", "", nil)

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
	// Should NOT contain the actual skill body content (only name/description)
	if strings.Contains(prompt, "Step 1: do this") {
		t.Error("skill body should not be in prompt")
	}
}

func TestBuildSystemPrompt_NoSkills(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Instructions.",
	}
	prompt := buildSystemPrompt(cfg, "", "", "", nil)
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

	prompt := buildSystemPrompt(cfg, "I am Agent X", "User likes Go", "- [x] Done\n- [ ] Next\n", nil)

	for _, want := range []string{"## Identity", "## Memories", "## Current Tasks", "Be kind.", "You are a helpful agent."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q in prompt", want)
		}
	}

	// Section preambles should include usage guidance
	if !strings.Contains(prompt, "memory_write") {
		t.Error("Memories section should include memory_write usage guidance")
	}
	if !strings.Contains(prompt, "todos tool") {
		t.Error("Tasks section should include todos tool usage guidance")
	}
}

func TestBuildSystemPrompt_EmptyOptionalSections(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Instructions here.",
	}

	prompt := buildSystemPrompt(cfg, "", "", "", nil)
	for _, absent := range []string{"## Identity", "## Memories", "## Current Tasks"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("empty section %q should not appear", absent)
		}
	}
	if !strings.Contains(prompt, "Instructions here.") {
		t.Error("missing prompt body")
	}
}

func TestBuildSystemPrompt_SecuritySection(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Do things.",
	}
	prompt := buildSystemPrompt(cfg, "", "", "", nil)
	if !strings.Contains(prompt, "## Security") {
		t.Error("Security section should always be present")
	}
	if !strings.Contains(prompt, "untrusted data") {
		t.Error("Security section should warn about untrusted tool results")
	}
}

func TestBuildSystemPrompt_SecretsSecurityGuidance(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Do things.",
	}
	prompt := buildSystemPrompt(cfg, "", "", "", []string{"API_KEY"})
	if !strings.Contains(prompt, "Never expose secret values") {
		t.Error("Secrets section should include guidance about not exposing values")
	}
}

func TestBuildSystemPrompt_SkillsPreamble(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Do things.",
		Skills: []config.SkillConfig{
			{Name: "review", Description: "Review code."},
		},
	}
	prompt := buildSystemPrompt(cfg, "", "", "", nil)
	// Should explain that descriptions are triggers, not full instructions
	if !strings.Contains(prompt, "triggers") {
		t.Error("Skills preamble should explain descriptions are triggers")
	}
}

// sectionIndex returns the index of a section header or content string,
// failing the test if it's not found.
func sectionIndex(t *testing.T, prompt, marker string) int {
	t.Helper()
	idx := strings.Index(prompt, marker)
	if idx < 0 {
		t.Fatalf("expected %q in prompt but not found", marker)
	}
	return idx
}

func TestBuildSystemPrompt_SectionOrdering(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Main instructions.",
		Soul:   "Be concise.",
		Tools:  "Use edit for small changes.",
		Skills: []config.SkillConfig{
			{Name: "deploy", Description: "Deploy code."},
		},
	}
	prompt := buildSystemPrompt(cfg, "I am agent-7", "User prefers Go", "- [ ] Fix bug", []string{"GH_TOKEN"})

	// Verify strict ordering: soul < identity < memories < todos < secrets < body < tools < skills < security
	soul := sectionIndex(t, prompt, "Be concise.")
	identity := sectionIndex(t, prompt, "## Identity")
	memories := sectionIndex(t, prompt, "## Memories")
	todos := sectionIndex(t, prompt, "## Current Tasks")
	secrets := sectionIndex(t, prompt, "## Available Secrets")
	body := sectionIndex(t, prompt, "Main instructions.")
	tools := sectionIndex(t, prompt, "## Tool Notes")
	skills := sectionIndex(t, prompt, "## Skills")
	security := sectionIndex(t, prompt, "## Security")

	pairs := []struct{ name string; a, b int }{
		{"soul < identity", soul, identity},
		{"identity < memories", identity, memories},
		{"memories < todos", memories, todos},
		{"todos < secrets", todos, secrets},
		{"secrets < body", secrets, body},
		{"body < tools", body, tools},
		{"tools < skills", tools, skills},
		{"skills < security", skills, security},
	}
	for _, p := range pairs {
		if p.a >= p.b {
			t.Errorf("ordering violated: %s (got %d >= %d)", p.name, p.a, p.b)
		}
	}
}

func TestBuildSystemPrompt_MinimalEphemeral(t *testing.T) {
	// An ephemeral agent with no soul, no skills, no tools, no secrets —
	// just a body and the security section.
	cfg := config.AgentConfig{
		Name:   "worker",
		Prompt: "Summarize the input.",
	}
	prompt := buildSystemPrompt(cfg, "", "", "", nil)

	// Should contain body and security
	if !strings.Contains(prompt, "Summarize the input.") {
		t.Error("missing prompt body")
	}
	if !strings.Contains(prompt, "## Security") {
		t.Error("missing security section")
	}

	// Should NOT contain any optional sections
	for _, absent := range []string{"## Identity", "## Memories", "## Current Tasks", "## Available Secrets", "## Tool Notes", "## Skills"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("section %q should not appear for minimal agent", absent)
		}
	}
}

func TestBuildSystemPrompt_FullPersistent(t *testing.T) {
	// A fully-loaded persistent agent with every section populated.
	cfg := config.AgentConfig{
		Name:   "coordinator",
		Prompt: "You are the coordinator.",
		Soul:   "Be helpful and precise.",
		Tools:  "Prefer edit over write_file.",
		Skills: []config.SkillConfig{
			{Name: "delegate", Description: "Delegate tasks to subagents."},
			{Name: "review", Description: "Review code changes."},
		},
	}
	prompt := buildSystemPrompt(cfg,
		"I am the coordinator agent.",
		"User is a Go developer.\nProject uses microservices.",
		"- [x] Set up repo\n- [ ] Write tests",
		[]string{"GITHUB_TOKEN", "SLACK_WEBHOOK"},
	)

	// All sections present
	for _, want := range []string{
		"Be helpful and precise.",
		"## Identity", "I am the coordinator agent.",
		"## Memories", "memory_write", "User is a Go developer.",
		"## Current Tasks", "todos tool", "Write tests",
		"## Available Secrets", "bash commands only", "`GITHUB_TOKEN`", "`SLACK_WEBHOOK`",
		"You are the coordinator.",
		"## Tool Notes", "Prefer edit",
		"## Skills", "triggers", "**delegate**", "**review**",
		"## Security", "untrusted data",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q in full persistent prompt", want)
		}
	}
}

func TestBuildSystemPrompt_EphemeralWithSecrets(t *testing.T) {
	// An ephemeral agent that has bash + secrets but no persistent sections.
	cfg := config.AgentConfig{
		Name:   "fetcher",
		Prompt: "Fetch data from the API.",
	}
	prompt := buildSystemPrompt(cfg, "", "", "", []string{"API_KEY"})

	if !strings.Contains(prompt, "## Available Secrets") {
		t.Error("secrets section should appear")
	}
	if !strings.Contains(prompt, "`API_KEY`") {
		t.Error("secret name should be listed")
	}
	// No persistent sections
	for _, absent := range []string{"## Identity", "## Memories", "## Current Tasks"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("section %q should not appear for ephemeral agent", absent)
		}
	}
}

func TestBuildSystemPrompt_SkillsAndToolNotes(t *testing.T) {
	cfg := config.AgentConfig{
		Name:   "test",
		Prompt: "Do work.",
		Tools:  "Always run tests after edits.",
		Skills: []config.SkillConfig{
			{Name: "lint", Description: "Lint the codebase."},
		},
	}
	prompt := buildSystemPrompt(cfg, "", "", "", nil)

	toolNotes := sectionIndex(t, prompt, "## Tool Notes")
	skills := sectionIndex(t, prompt, "## Skills")
	security := sectionIndex(t, prompt, "## Security")

	if toolNotes >= skills {
		t.Error("Tool Notes should appear before Skills")
	}
	if skills >= security {
		t.Error("Skills should appear before Security")
	}
}

func TestBuildSystemPrompt_NoDoubleBlankLines(t *testing.T) {
	// With various combos of present/absent sections, there should be
	// no triple+ newlines (which would indicate empty section artifacts).
	cases := []struct {
		name string
		cfg  config.AgentConfig
		id   string
		mem  string
		todo string
		sec  []string
	}{
		{"bare", config.AgentConfig{Prompt: "Go."}, "", "", "", nil},
		{"soul+body", config.AgentConfig{Prompt: "Go.", Soul: "Hi."}, "", "", "", nil},
		{"memory only", config.AgentConfig{Prompt: "Go."}, "", "stuff", "", nil},
		{"todos only", config.AgentConfig{Prompt: "Go."}, "", "", "- [ ] x", nil},
		{"secrets only", config.AgentConfig{Prompt: "Go."}, "", "", "", []string{"K"}},
		{"all", config.AgentConfig{Prompt: "Go.", Soul: "Hi.", Tools: "T.", Skills: []config.SkillConfig{{Name: "s", Description: "d"}}}, "id", "mem", "todo", []string{"K"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompt := buildSystemPrompt(tc.cfg, tc.id, tc.mem, tc.todo, tc.sec)
			if strings.Contains(prompt, "\n\n\n") {
				t.Errorf("prompt contains triple newline:\n%s", prompt)
			}
		})
	}
}
