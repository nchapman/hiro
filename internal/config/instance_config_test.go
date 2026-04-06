package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstanceConfig_NotExists(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadInstanceConfig(dir)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if cfg.Model != "" || cfg.ReasoningEffort != "" || cfg.Channels != nil {
		t.Errorf("expected zero-value config, got %+v", cfg)
	}
}

func TestLoadInstanceConfig_Empty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadInstanceConfig(dir)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if cfg.Model != "" {
		t.Errorf("expected empty model, got %q", cfg.Model)
	}
}

func TestSaveAndLoadInstanceConfig_ModelOnly(t *testing.T) {
	dir := t.TempDir()
	want := InstanceConfig{
		Model:           "anthropic/claude-sonnet-4",
		ReasoningEffort: "high",
	}
	if err := SaveInstanceConfig(dir, want); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	got, err := LoadInstanceConfig(dir)
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
	want := InstanceConfig{
		Model:           "openrouter/anthropic/claude-sonnet-4",
		ReasoningEffort: "medium",
		Channels: &InstanceChannelsConfig{
			Telegram: &InstanceTelegramConfig{
				BotToken:     "${TELEGRAM_BOT}",
				AllowedChats: []int64{12345, 67890},
			},
			Slack: &InstanceSlackConfig{
				BotToken:        "${SLACK_BOT}",
				SigningSecret:   "${SLACK_SIGN}",
				AllowedChannels: []string{"C123", "C456"},
			},
		},
	}
	if err := SaveInstanceConfig(dir, want); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	got, err := LoadInstanceConfig(dir)
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
	if len(got.Channels.Telegram.AllowedChats) != 2 {
		t.Errorf("Telegram.AllowedChats: got %v", got.Channels.Telegram.AllowedChats)
	}
	if got.Channels.Slack == nil {
		t.Fatal("Slack is nil")
	}
	if got.Channels.Slack.BotToken != "${SLACK_BOT}" {
		t.Errorf("Slack.BotToken: got %q", got.Channels.Slack.BotToken)
	}
	if len(got.Channels.Slack.AllowedChannels) != 2 {
		t.Errorf("Slack.AllowedChannels: got %v", got.Channels.Slack.AllowedChannels)
	}
}

func TestSaveInstanceConfig_Permissions(t *testing.T) {
	dir := t.TempDir()
	if err := SaveInstanceConfig(dir, InstanceConfig{Model: "test"}); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.yaml"))
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
	if err := SaveInstanceConfig(dir, InstanceConfig{Model: "first"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := SaveInstanceConfig(dir, InstanceConfig{Model: "second"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := LoadInstanceConfig(dir)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if got.Model != "second" {
		t.Errorf("expected overwrite, got %q", got.Model)
	}
}

func TestSaveAndLoadInstanceConfig_WithTools(t *testing.T) {
	dir := t.TempDir()
	want := InstanceConfig{
		AllowedTools:    []string{"Bash", "Read", "Write", "Glob"},
		DisallowedTools: []string{"Bash(rm *)"},
	}
	if err := SaveInstanceConfig(dir, want); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}
	got, err := LoadInstanceConfig(dir)
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

func TestIsInstanceConfigFile(t *testing.T) {
	instDir := "/instances/abc123"
	if !IsInstanceConfigFile("/instances/abc123/config.yaml", instDir) {
		t.Error("should match config.yaml in instance root")
	}
	if IsInstanceConfigFile("/instances/abc123/sessions/xyz/config.yaml", instDir) {
		t.Error("should not match config.yaml in subdirectory")
	}
	if IsInstanceConfigFile("/instances/abc123/persona.md", instDir) {
		t.Error("should not match other files")
	}
}
