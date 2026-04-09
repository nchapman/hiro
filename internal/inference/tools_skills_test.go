package inference

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

func TestIsUnderAllowedDir(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		allowedDirs []string
		want        bool
	}{
		{
			name:        "under allowed dir",
			path:        "/workspace/agents/test/SKILL.md",
			allowedDirs: []string{"/workspace"},
			want:        true,
		},
		{
			name:        "not under allowed dir",
			path:        "/etc/passwd",
			allowedDirs: []string{"/workspace"},
			want:        false,
		},
		{
			name:        "multiple allowed dirs",
			path:        "/skills/shared/test.md",
			allowedDirs: []string{"/workspace", "/skills"},
			want:        true,
		},
		{
			name:        "empty allowed dirs denies all paths",
			path:        "/anywhere/file.md",
			allowedDirs: nil,
			want:        false,
		},
		{
			name:        "all empty strings denies all paths",
			path:        "/anywhere/file.md",
			allowedDirs: []string{"", ""},
			want:        false,
		},
		{
			name:        "exact dir match without trailing slash",
			path:        "/workspace",
			allowedDirs: []string{"/workspace"},
			want:        false, // path must be under dir, not the dir itself
		},
		{
			name:        "path traversal attempt",
			path:        "/workspace/../etc/passwd",
			allowedDirs: []string{"/workspace"},
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUnderAllowedDir(tt.path, tt.allowedDirs)
			if got != tt.want {
				t.Errorf("isUnderAllowedDir(%q, %v) = %v, want %v",
					tt.path, tt.allowedDirs, got, tt.want)
			}
		})
	}
}

func TestAppendBundledResources(t *testing.T) {
	dir := t.TempDir()

	// Create a SKILL.md and some bundled resources.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helper.sh"), []byte("#!/bin/bash"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	appendBundledResources(&sb, dir)

	result := sb.String()
	if !strings.Contains(result, "## Bundled Resources") {
		t.Error("expected Bundled Resources header")
	}
	if !strings.Contains(result, "helper.sh") {
		t.Error("expected helper.sh in resources")
	}
	if !strings.Contains(result, "README.txt") {
		t.Error("expected README.txt in resources")
	}
	// SKILL.md should be excluded.
	if strings.Contains(result, "SKILL.md") {
		t.Error("SKILL.md should be excluded from bundled resources")
	}
}

func TestAppendBundledResources_WithSubdir(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.Mkdir(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "deploy.sh"), []byte("#!/bin/bash"), 0o644); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	appendBundledResources(&sb, dir)

	result := sb.String()
	if !strings.Contains(result, "scripts/") {
		t.Error("expected scripts/ directory listing")
	}
	if !strings.Contains(result, "scripts/deploy.sh") {
		t.Error("expected scripts/deploy.sh in resources")
	}
}

func TestAppendBundledResources_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	appendBundledResources(&sb, dir)

	// Only SKILL.md, which is excluded, so no resources section.
	if strings.Contains(sb.String(), "Bundled Resources") {
		t.Error("should not show Bundled Resources when only SKILL.md exists")
	}
}

func TestAppendBundledResources_Truncation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create more than 50 files to trigger truncation.
	for i := range 55 {
		name := filepath.Join(dir, strings.Repeat("f", 5)+string(rune('a'+i/26))+string(rune('a'+i%26))+".txt")
		if err := os.WriteFile(name, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var sb strings.Builder
	appendBundledResources(&sb, dir)

	if !strings.Contains(sb.String(), "truncated") {
		t.Error("expected truncation notice for >50 resources")
	}
}

func TestBuildSkillTool_SkillNotFound(t *testing.T) {
	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "existing-skill"},
		},
	}

	tool := buildSkillTool(cfg, nil, nil, testLogger)

	ctx := context.Background()
	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":"nonexistent"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Error("expected error for nonexistent skill")
	}
	if !strings.Contains(resp.Content, "not found") {
		t.Errorf("expected 'not found' in error, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "existing-skill") {
		t.Errorf("expected available skill names in error, got: %s", resp.Content)
	}
}

func TestBuildSkillTool_EmptyName(t *testing.T) {
	cfg := &config.AgentConfig{}
	tool := buildSkillTool(cfg, nil, nil, testLogger)

	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":""}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Error("expected error for empty name")
	}
}

func TestBuildSkillTool_SymlinkEscape_Rejected(t *testing.T) {
	allowedDir := resolvedTempDir(t)
	outsideDir := resolvedTempDir(t)

	// Create the real skill file outside the allowed directory.
	externalSkill := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(externalSkill, []byte("---\nname: evil\n---\nStolen content."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside the allowed directory pointing outside.
	symlinkPath := filepath.Join(allowedDir, "evil.md")
	if err := os.Symlink(externalSkill, symlinkPath); err != nil {
		t.Fatal(err)
	}

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "evil", Path: symlinkPath},
		},
	}

	tool := buildSkillTool(cfg, []string{allowedDir}, nil, testLogger)

	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":"evil"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Error("expected error for symlink pointing outside allowed dirs")
	}
	if !strings.Contains(resp.Content, "outside allowed") {
		t.Errorf("expected 'outside allowed' in error, got: %s", resp.Content)
	}
}

func TestBuildSkillTool_OutsideAllowedDirs(t *testing.T) {
	skillPath := filepath.Join(t.TempDir(), "skill.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: test\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "test", Path: skillPath},
		},
	}

	// Restrict to a different directory.
	tool := buildSkillTool(cfg, []string{"/nonexistent/allowed"}, nil, testLogger)

	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":"test"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Error("expected error for skill outside allowed dirs")
	}
	if !strings.Contains(resp.Content, "outside allowed") {
		t.Errorf("expected 'outside allowed' in error, got: %s", resp.Content)
	}
}

// resolvedTempDir returns the symlink-resolved path of t.TempDir(),
// needed because macOS /tmp -> /private/tmp and EvalSymlinks resolves it.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestBuildSkillTool_ReadsSkillBody(t *testing.T) {
	dir := resolvedTempDir(t)
	skillPath := filepath.Join(dir, "test-skill.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: test-skill\n---\nThese are the skill instructions."), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "test-skill", Path: skillPath},
		},
	}

	tool := buildSkillTool(cfg, []string{dir}, nil, testLogger)

	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":"test-skill"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "These are the skill instructions.") {
		t.Errorf("expected skill body in response, got: %s", resp.Content)
	}
}

func TestBuildSkillTool_DirectorySkillWithResources(t *testing.T) {
	dir := resolvedTempDir(t)
	skillDir := filepath.Join(dir, "my-skill")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: my-skill\n---\nSkill body here."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "helper.py"), []byte("print('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "my-skill", Path: skillPath},
		},
	}

	tool := buildSkillTool(cfg, []string{dir}, nil, testLogger)

	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":"my-skill"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Skill body here.") {
		t.Error("expected skill body in response")
	}
	if !strings.Contains(resp.Content, "helper.py") {
		t.Error("expected bundled resource listing")
	}
}

func TestBuildSkillTool_CallsOnExpand(t *testing.T) {
	dir := resolvedTempDir(t)
	skillPath := filepath.Join(dir, "deploy.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: deploy\n---\nDeploy instructions."), 0o644); err != nil {
		t.Fatal(err)
	}

	var expandedSkill string
	onExpand := func(skill *config.SkillConfig) error {
		expandedSkill = skill.Name
		return nil
	}

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "deploy", Path: skillPath, AllowedTools: []string{"Bash"}},
		},
	}

	tool := buildSkillTool(cfg, []string{dir}, onExpand, testLogger)

	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":"deploy"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if expandedSkill != "deploy" {
		t.Errorf("expected onExpand called with 'deploy', got %q", expandedSkill)
	}
}

func TestBuildSkillTool_OnExpandNotCalledWithoutAllowedTools(t *testing.T) {
	dir := resolvedTempDir(t)
	skillPath := filepath.Join(dir, "info.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: info\n---\nInfo only."), 0o644); err != nil {
		t.Fatal(err)
	}

	called := false
	onExpand := func(skill *config.SkillConfig) error {
		called = true
		return nil
	}

	cfg := &config.AgentConfig{
		Skills: []config.SkillConfig{
			{Name: "info", Path: skillPath}, // no AllowedTools
		},
	}

	tool := buildSkillTool(cfg, []string{dir}, onExpand, testLogger)

	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  "Skill",
		Input: `{"name":"info"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if called {
		t.Error("onExpand should not be called when skill has no AllowedTools")
	}
}
