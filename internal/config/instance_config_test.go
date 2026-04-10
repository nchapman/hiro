package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstanceConfig_NotExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")
	cfg, err := LoadInstanceConfig(path)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if cfg.Model != "" || cfg.ReasoningEffort != "" || cfg.Channels != nil {
		t.Errorf("expected zero-value config, got %+v", cfg)
	}
}

func TestLoadInstanceConfig_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadInstanceConfig(path)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if cfg.Model != "" {
		t.Errorf("expected empty model, got %q", cfg.Model)
	}
}

func TestSaveAndLoadInstanceConfig_ModelOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := InstanceConfig{
		Model:           "anthropic/claude-sonnet-4",
		ReasoningEffort: "high",
	}
	if err := SaveInstanceConfig(path, want); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	got, err := LoadInstanceConfig(path)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if got.Model != want.Model {
		t.Errorf("Model: got %q, want %q", got.Model, want.Model)
	}
	if got.ReasoningEffort != want.ReasoningEffort {
		t.Errorf("ReasoningEffort: got %q, want %q", got.ReasoningEffort, want.ReasoningEffort)
	}
}

func TestSaveAndLoadInstanceConfig_Full(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := InstanceConfig{
		Model:           "openrouter/anthropic/claude-sonnet-4",
		ReasoningEffort: "medium",
		Channels: &InstanceChannelsConfig{
			Telegram: &InstanceTelegramConfig{
				BotToken: "${TELEGRAM_BOT}",
			},
			Slack: &InstanceSlackConfig{
				BotToken:      "${SLACK_BOT}",
				SigningSecret: "${SLACK_SIGN}",
			},
		},
	}
	if err := SaveInstanceConfig(path, want); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	got, err := LoadInstanceConfig(path)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if got.Model != want.Model {
		t.Errorf("Model: got %q, want %q", got.Model, want.Model)
	}
	if got.Channels == nil {
		t.Fatal("Channels is nil")
	}
	if got.Channels.Telegram == nil {
		t.Fatal("Telegram is nil")
	}
	if got.Channels.Telegram.BotToken != "${TELEGRAM_BOT}" {
		t.Errorf("Telegram.BotToken: got %q", got.Channels.Telegram.BotToken)
	}
	if got.Channels.Slack == nil {
		t.Fatal("Slack is nil")
	}
	if got.Channels.Slack.BotToken != "${SLACK_BOT}" {
		t.Errorf("Slack.BotToken: got %q", got.Channels.Slack.BotToken)
	}
}

func TestSaveInstanceConfig_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := SaveInstanceConfig(path, InstanceConfig{Model: "test"}); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected 0o600 permissions, got %o", perm)
	}
}

func TestSaveInstanceConfig_Overwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := SaveInstanceConfig(path, InstanceConfig{Model: "first"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := SaveInstanceConfig(path, InstanceConfig{Model: "second"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := LoadInstanceConfig(path)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if got.Model != "second" {
		t.Errorf("expected overwrite, got %q", got.Model)
	}
}

func TestSaveAndLoadInstanceConfig_WithTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := InstanceConfig{
		AllowedTools:    []string{"Bash", "Read", "Write", "Glob"},
		DisallowedTools: []string{"Bash(rm *)"},
	}
	if err := SaveInstanceConfig(path, want); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	got, err := LoadInstanceConfig(path)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if len(got.AllowedTools) != 4 {
		t.Errorf("AllowedTools: got %v, want 4 items", got.AllowedTools)
	}
	if got.AllowedTools[0] != "Bash" {
		t.Errorf("AllowedTools[0]: got %q, want %q", got.AllowedTools[0], "Bash")
	}
	if len(got.DisallowedTools) != 1 || got.DisallowedTools[0] != "Bash(rm *)" {
		t.Errorf("DisallowedTools: got %v", got.DisallowedTools)
	}
}

func TestSaveInstanceConfig_CreatesDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config", "instances", "test-id.yaml")
	if err := SaveInstanceConfig(path, InstanceConfig{Model: "test"}); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	got, err := LoadInstanceConfig(path)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if got.Model != "test" {
		t.Errorf("Model: got %q, want %q", got.Model, "test")
	}
}

func TestInstanceConfigPath(t *testing.T) {
	got := InstanceConfigPath("/hiro", "abc-123")
	want := "/hiro/config/instances/abc-123.yaml"
	if got != want {
		t.Errorf("InstanceConfigPath: got %q, want %q", got, want)
	}
}
