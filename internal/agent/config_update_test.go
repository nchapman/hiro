package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
)

// --- toolsEqual tests ---

func TestToolsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string]bool
		want bool
	}{
		{"both nil", nil, nil, true},
		{"nil vs empty", nil, map[string]bool{}, false},
		{"empty vs nil", map[string]bool{}, nil, false},
		{"both empty", map[string]bool{}, map[string]bool{}, true},
		{"equal", map[string]bool{"bash": true, "grep": true}, map[string]bool{"bash": true, "grep": true}, true},
		{"different values", map[string]bool{"bash": true}, map[string]bool{"bash": false}, false},
		{"different keys", map[string]bool{"bash": true}, map[string]bool{"grep": true}, false},
		{"different lengths", map[string]bool{"bash": true, "grep": true}, map[string]bool{"bash": true}, false},
		{"subset", map[string]bool{"bash": true}, map[string]bool{"bash": true, "grep": true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("toolsEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// --- ApplyConfigUpdate / consumePendingUpdate tests ---

func TestApplyConfigUpdate_StoresAndConsumes(t *testing.T) {
	a := &Agent{logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}

	// No pending update initially.
	if got := a.consumePendingUpdate(); got != nil {
		t.Fatal("expected nil before any update")
	}

	// Store an update.
	update := ipc.ConfigUpdate{Model: "new-model", Provider: "anthropic"}
	a.ApplyConfigUpdate(update)

	// Consume it.
	got := a.consumePendingUpdate()
	if got == nil {
		t.Fatal("expected pending update")
	}
	if got.Model != "new-model" {
		t.Errorf("model = %q, want %q", got.Model, "new-model")
	}

	// Second consume should be nil (already consumed).
	if again := a.consumePendingUpdate(); again != nil {
		t.Fatal("expected nil after consume")
	}
}

func TestApplyConfigUpdate_LatestWins(t *testing.T) {
	a := &Agent{logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}

	// Push two updates before consuming.
	a.ApplyConfigUpdate(ipc.ConfigUpdate{Model: "model-1"})
	a.ApplyConfigUpdate(ipc.ConfigUpdate{Model: "model-2"})

	got := a.consumePendingUpdate()
	if got == nil || got.Model != "model-2" {
		t.Errorf("expected latest update (model-2), got %v", got)
	}
}

// --- applyConfigUpdate: tool swap tests ---

func TestApplyConfigUpdate_ToolSwap(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	a := &Agent{
		config:          config.AgentConfig{Name: "test"},
		workingDir:      dir,
		allowedTools:    map[string]bool{"bash": true, "read_file": true, "grep": true},
		currentModel:    "same-model",
		currentProvider: "same-provider",
		logger:          logger,
	}

	// Push an update that changes tools but not model.
	update := &ipc.ConfigUpdate{
		EffectiveTools: map[string]bool{"bash": true, "read_file": true}, // removed grep
		Model:          "same-model",
		Provider:       "same-provider",
	}

	result := fantasy.PrepareStepResult{}
	a.applyConfigUpdate(context.Background(), update, &result)

	// Tools should have been rebuilt.
	if result.Tools == nil {
		t.Fatal("expected tools to be rebuilt")
	}

	// Verify grep is no longer in the tool set.
	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Info().Name] = true
	}
	if toolNames["grep"] {
		t.Error("grep should have been filtered out")
	}
	if !toolNames["bash"] {
		t.Error("bash should still be present")
	}
	if !toolNames["read_file"] {
		t.Error("read_file should still be present")
	}

	// Model should NOT have been swapped.
	if result.Model != nil {
		t.Error("model should not have changed")
	}
}

func TestApplyConfigUpdate_NoChangeNoop(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tools := map[string]bool{"bash": true}
	a := &Agent{
		config:          config.AgentConfig{Name: "test"},
		workingDir:      dir,
		allowedTools:    tools,
		currentModel:    "my-model",
		currentProvider: "anthropic",
		logger:          logger,
	}

	update := &ipc.ConfigUpdate{
		EffectiveTools: map[string]bool{"bash": true}, // same
		Model:          "my-model",                     // same
		Provider:       "anthropic",                    // same
	}

	result := fantasy.PrepareStepResult{}
	a.applyConfigUpdate(context.Background(), update, &result)

	if result.Model != nil {
		t.Error("model should not change on noop")
	}
	if result.Tools != nil {
		t.Error("tools should not change on noop")
	}
}

func TestApplyConfigUpdate_ModelSwapFailure(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	a := &Agent{
		config:          config.AgentConfig{Name: "test"},
		workingDir:      dir,
		allowedTools:    map[string]bool{"bash": true},
		currentModel:    "old-model",
		currentProvider: "anthropic",
		logger:          logger,
	}

	// Push update with an invalid provider — CreateLanguageModel should fail.
	update := &ipc.ConfigUpdate{
		EffectiveTools: map[string]bool{"bash": true},
		Model:          "new-model",
		Provider:       "nonexistent-provider",
		APIKey:         "fake-key",
	}

	result := fantasy.PrepareStepResult{}
	a.applyConfigUpdate(context.Background(), update, &result)

	// Model should NOT have changed (graceful failure).
	if result.Model != nil {
		t.Error("model should not change when CreateLanguageModel fails")
	}
	if a.currentModel != "old-model" {
		t.Errorf("currentModel should remain %q, got %q", "old-model", a.currentModel)
	}
	if a.currentProvider != "anthropic" {
		t.Errorf("currentProvider should remain %q, got %q", "anthropic", a.currentProvider)
	}
}

func TestApplyConfigUpdate_UnrestrictedToRestricted(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Start unrestricted (nil tools).
	a := &Agent{
		config:          config.AgentConfig{Name: "test"},
		workingDir:      dir,
		allowedTools:    nil, // unrestricted
		currentModel:    "model",
		currentProvider: "anthropic",
		logger:          logger,
	}

	// Restrict to just bash.
	update := &ipc.ConfigUpdate{
		EffectiveTools: map[string]bool{"bash": true},
		Model:          "model",
		Provider:       "anthropic",
	}

	result := fantasy.PrepareStepResult{}
	a.applyConfigUpdate(context.Background(), update, &result)

	if result.Tools == nil {
		t.Fatal("expected tools to be rebuilt when switching from unrestricted to restricted")
	}

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Info().Name] = true
	}
	if !toolNames["bash"] {
		t.Error("bash should be present")
	}
	if toolNames["grep"] {
		t.Error("grep should be filtered out in restricted mode")
	}
}

// --- Agent text reload in-session ---

func TestCurrentSystemPrompt_ReloadsTextFromDisk(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents", "test")
	os.MkdirAll(agentDir, 0755)

	// Write initial agent definition.
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: test\n---\nOriginal instructions."), 0644)
	os.WriteFile(filepath.Join(agentDir, "soul.md"), []byte("Be kind."), 0644)
	os.WriteFile(filepath.Join(agentDir, "tools.md"), []byte("Use bash carefully."), 0644)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := &Agent{
		config: config.AgentConfig{
			Name:   "test",
			Prompt: "Original instructions.",
			Soul:   "Be kind.",
			Tools:  "Use bash carefully.",
		},
		agentDefDir: agentDir,
		logger:      logger,
	}

	// First call — should return current text.
	prompt := a.currentSystemPrompt()
	if !contains(prompt, "Original instructions.") {
		t.Errorf("expected original instructions in prompt, got:\n%s", prompt)
	}
	if !contains(prompt, "Be kind.") {
		t.Errorf("expected soul in prompt")
	}

	// Edit files on disk.
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: test\n---\nUpdated instructions."), 0644)
	os.WriteFile(filepath.Join(agentDir, "soul.md"), []byte("Be brave."), 0644)
	os.WriteFile(filepath.Join(agentDir, "tools.md"), []byte("Prefer grep."), 0644)

	// Second call — should pick up new text.
	prompt = a.currentSystemPrompt()
	if !contains(prompt, "Updated instructions.") {
		t.Errorf("expected updated instructions in prompt, got:\n%s", prompt)
	}
	if contains(prompt, "Original instructions.") {
		t.Error("should not contain old instructions")
	}
	if !contains(prompt, "Be brave.") {
		t.Error("expected updated soul")
	}
	if !contains(prompt, "Prefer grep.") {
		t.Error("expected updated tool notes")
	}
}

func TestCurrentSystemPrompt_ReloadsSkillsFromDisk(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents", "test")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("---\nname: test\n---\nInstructions."), 0644)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := &Agent{
		config: config.AgentConfig{
			Name:   "test",
			Prompt: "Instructions.",
		},
		agentDefDir: agentDir,
		logger:      logger,
	}

	// No skills initially.
	prompt := a.currentSystemPrompt()
	if contains(prompt, "Skills") {
		t.Error("should not contain Skills section initially")
	}

	// Add a skill on disk.
	skillsDir := filepath.Join(agentDir, "skills")
	os.MkdirAll(skillsDir, 0755)
	os.WriteFile(filepath.Join(skillsDir, "search.md"), []byte("---\nname: search\ndescription: Search the web\n---\nSearch instructions."), 0644)

	// Should pick up new skill.
	prompt = a.currentSystemPrompt()
	if !contains(prompt, "search") {
		t.Errorf("expected search skill in prompt, got:\n%s", prompt)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
