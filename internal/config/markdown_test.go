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

func TestLoadAgentDir_DefaultMode(t *testing.T) {
	dir := t.TempDir()
	agentMD := `---
name: test
---

No mode specified.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Mode != ModePersistent {
		t.Errorf("mode = %q, want %q", agent.Mode, ModePersistent)
	}
}

func TestLoadAgentDir_InvalidMode(t *testing.T) {
	dir := t.TempDir()
	agentMD := `---
name: test
mode: persistant
---

Typo in mode.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAgentDir(dir)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestLoadAgentDir_ExplicitMode(t *testing.T) {
	dir := t.TempDir()
	agentMD := `---
name: worker
mode: ephemeral
---

An ephemeral agent.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Mode != ModeEphemeral {
		t.Errorf("mode = %q, want %q", agent.Mode, ModeEphemeral)
	}
}

func TestLoadAgentDir_CoordinatorMode(t *testing.T) {
	dir := t.TempDir()
	agentMD := `---
name: coordinator
mode: coordinator
tools: [bash, read_file]
---

A coordinator agent.`
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Mode != ModeCoordinator {
		t.Errorf("mode = %q, want %q", agent.Mode, ModeCoordinator)
	}
}

func TestAgentMode_IsPersistent(t *testing.T) {
	tests := []struct {
		mode AgentMode
		want bool
	}{
		{ModePersistent, true},
		{ModeCoordinator, true},
		{ModeEphemeral, false},
	}
	for _, tt := range tests {
		if got := tt.mode.IsPersistent(); got != tt.want {
			t.Errorf("%q.IsPersistent() = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestLoadAgentDir_WithSoul(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "agent.md"), []byte("---\nname: test\n---\nInstructions."), 0644)
	os.WriteFile(filepath.Join(dir, "soul.md"), []byte("Be warm and curious."), 0644)

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Soul != "Be warm and curious." {
		t.Errorf("soul = %q, want %q", agent.Soul, "Be warm and curious.")
	}
}

func TestLoadAgentDir_WithTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "agent.md"), []byte("---\nname: test\n---\nInstructions."), 0644)
	os.WriteFile(filepath.Join(dir, "tools.md"), []byte("Use grep for searching."), 0644)

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Tools != "Use grep for searching." {
		t.Errorf("tools = %q, want %q", agent.Tools, "Use grep for searching.")
	}
}

func TestLoadAgentDir_WithoutSoulAndTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "agent.md"), []byte("---\nname: test\n---\nInstructions."), 0644)

	agent, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Soul != "" {
		t.Errorf("soul should be empty, got %q", agent.Soul)
	}
	if agent.Tools != "" {
		t.Errorf("tools should be empty, got %q", agent.Tools)
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

func TestValidateSkillName(t *testing.T) {
	valid := []string{"search", "code-review", "my-skill-123", "a", "a-b-c"}
	for _, name := range valid {
		if err := ValidateSkillName(name); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", name, err)
		}
	}

	invalid := []struct {
		name, wantErr string
	}{
		{"", "required"},
		{"has spaces", "kebab-case"},
		{"has_underscores", "kebab-case"},
		{"-leading-hyphen", "kebab-case"},
		{"trailing-hyphen-", "kebab-case"},
		{"double--hyphen", "kebab-case"},
		{"CAPS", "kebab-case"},
		{strings.Repeat("a", 65), "64 character"},
	}
	for _, tc := range invalid {
		err := ValidateSkillName(tc.name)
		if err == nil {
			t.Errorf("expected error for %q", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("ValidateSkillName(%q) = %v, want error containing %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestFrontmatter_StringMap(t *testing.T) {
	fm := Frontmatter{
		"metadata": map[string]any{"author": "test", "version": "1.0"},
	}
	m := fm.StringMap("metadata")
	if m["author"] != "test" || m["version"] != "1.0" {
		t.Errorf("got %v", m)
	}
	if fm.StringMap("missing") != nil {
		t.Error("expected nil for missing key")
	}
	fm2 := Frontmatter{"metadata": "not a map"}
	if fm2.StringMap("metadata") != nil {
		t.Error("expected nil for non-map value")
	}
}

func TestLoadSkills_ExtraMetadata(t *testing.T) {
	dir := t.TempDir()
	skillMD := `---
name: pdf-tools
description: Extract text and tables from PDFs.
license: Apache-2.0
compatibility: Requires python 3.8+
metadata:
  author: example-org
  version: "1.0"
---

Instructions for PDF processing.`
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "pdf-tools.md"), []byte(skillMD), 0644)

	skills, err := LoadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.License != "Apache-2.0" {
		t.Errorf("license = %q", s.License)
	}
	if s.Compatibility != "Requires python 3.8+" {
		t.Errorf("compatibility = %q", s.Compatibility)
	}
	if s.Metadata["author"] != "example-org" || s.Metadata["version"] != "1.0" {
		t.Errorf("metadata = %v", s.Metadata)
	}
	if s.Path == "" || !filepath.IsAbs(s.Path) {
		t.Errorf("expected absolute path, got %q", s.Path)
	}
}

func TestLoadSkills_DescriptionRequired(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "no-desc.md"), []byte("---\nname: no-desc\n---\nBody."), 0644)

	_, err := LoadSkills(filepath.Join(dir, "skills"))
	if err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("expected description error, got %v", err)
	}
}

func TestLoadSkills_NameValidation(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "bad.md"), []byte("---\nname: BAD NAME\ndescription: desc\n---\nBody."), 0644)

	_, err := LoadSkills(filepath.Join(dir, "skills"))
	if err == nil || !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("expected kebab-case error, got %v", err)
	}
}

func TestLoadSkills_DescriptionTooLong(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "long-desc.md"),
		[]byte("---\nname: long-desc\ndescription: "+strings.Repeat("x", 1025)+"\n---\nBody."), 0644)

	_, err := LoadSkills(filepath.Join(dir, "skills"))
	if err == nil || !strings.Contains(err.Error(), "1024") {
		t.Fatalf("expected 1024 limit error, got %v", err)
	}
}

func TestLoadSkills_SkillDirectory(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "my-skill")
	os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755)
	os.MkdirAll(filepath.Join(skillDir, "references"), 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: A directory-based skill.
---

Read references/guide.md for details.`), 0644)
	os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"), []byte("#!/bin/bash\necho hi"), 0755)

	skills, err := LoadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "my-skill" {
		t.Fatalf("expected 1 skill named my-skill, got %v", skills)
	}
	if !strings.Contains(skills[0].Path, "SKILL.md") {
		t.Errorf("path should contain SKILL.md, got %q", skills[0].Path)
	}
}

func TestLoadSkills_DirectoryNameMismatch(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills", "my-skill"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "my-skill", "SKILL.md"),
		[]byte("---\nname: wrong-name\ndescription: Mismatched.\n---\nBody."), 0644)

	_, err := LoadSkills(filepath.Join(dir, "skills"))
	if err == nil || !strings.Contains(err.Error(), "must match directory") {
		t.Fatalf("expected directory match error, got %v", err)
	}
}

func TestLoadSkills_DirectoryWithoutSKILLMD(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills", "empty-dir"), 0755)

	skills, err := LoadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestLoadSkills_MixedFormats(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	os.WriteFile(filepath.Join(skillsDir, "flat-skill.md"), []byte("---\nname: flat-skill\ndescription: Flat.\n---\nBody."), 0644)
	os.MkdirAll(filepath.Join(skillsDir, "dir-skill"), 0755)
	os.WriteFile(filepath.Join(skillsDir, "dir-skill", "SKILL.md"), []byte("---\nname: dir-skill\ndescription: Dir.\n---\nBody."), 0644)

	skills, err := LoadSkills(skillsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
}

func TestLoadSkills_NonexistentDir(t *testing.T) {
	skills, err := LoadSkills("/nonexistent/skills")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skills != nil {
		t.Errorf("expected nil, got %v", skills)
	}
}

func TestMergeSkills(t *testing.T) {
	agent := []SkillConfig{
		{Name: "search", Description: "Agent search"},
		{Name: "code", Description: "Agent code"},
	}
	shared := []SkillConfig{
		{Name: "search", Description: "Shared search"},
		{Name: "deploy", Description: "Shared deploy"},
	}

	merged := MergeSkills(agent, shared)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged skills, got %d", len(merged))
	}
	for _, s := range merged {
		if s.Name == "search" && s.Description != "Agent search" {
			t.Errorf("agent skill should take precedence, got %q", s.Description)
		}
	}
	found := false
	for _, s := range merged {
		if s.Name == "deploy" {
			found = true
		}
	}
	if !found {
		t.Error("shared 'deploy' should be in merged result")
	}
}

func TestLoadSkills_CompatibilityTooLong(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "long-compat.md"),
		[]byte("---\nname: long-compat\ndescription: desc\ncompatibility: "+strings.Repeat("x", 501)+"\n---\nBody."), 0644)

	_, err := LoadSkills(filepath.Join(dir, "skills"))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 limit error, got %v", err)
	}
}

func TestLoadSkills_PathIsAbsolute(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "my-skill.md"),
		[]byte("---\nname: my-skill\ndescription: desc\n---\nBody."), 0644)

	skills, err := LoadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if !filepath.IsAbs(skills[0].Path) {
		t.Errorf("expected absolute path, got %q", skills[0].Path)
	}
}

func TestMergeSkills_EmptyInputs(t *testing.T) {
	// Both empty
	merged := MergeSkills(nil, nil)
	if len(merged) != 0 {
		t.Errorf("expected 0, got %d", len(merged))
	}

	// Only shared
	shared := []SkillConfig{{Name: "a", Description: "A"}}
	merged = MergeSkills(nil, shared)
	if len(merged) != 1 || merged[0].Name != "a" {
		t.Errorf("expected shared skill, got %v", merged)
	}

	// Only agent
	agent := []SkillConfig{{Name: "b", Description: "B"}}
	merged = MergeSkills(agent, nil)
	if len(merged) != 1 || merged[0].Name != "b" {
		t.Errorf("expected agent skill, got %v", merged)
	}
}
