package controlplane

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestTelegramConfig_Nil(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	if cfg := cp.TelegramConfig(); cfg != nil {
		t.Errorf("expected nil, got %+v", cfg)
	}
}

func TestTelegramConfig_Configured(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{
		Channels: ChannelsConfig{
			Telegram: &TelegramChannelConfig{
				BotToken:     "${MY_TOKEN}",
				Instance:     "operator",
				AllowedChats: []int64{100, 200},
			},
		},
	}}

	cfg := cp.TelegramConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.BotToken != "${MY_TOKEN}" {
		t.Errorf("bot_token = %q", cfg.BotToken)
	}
	if cfg.Instance != "operator" {
		t.Errorf("instance = %q", cfg.Instance)
	}
	if len(cfg.AllowedChats) != 2 {
		t.Errorf("allowed_chats = %v", cfg.AllowedChats)
	}
}

func TestResolveSecret_LiteralValue(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	// Not a reference — return as-is.
	if v := cp.ResolveSecret("plain-token"); v != "plain-token" {
		t.Errorf("got %q, want %q", v, "plain-token")
	}
}

func TestResolveSecret_Reference(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()
	cp.config.Secrets["MY_TOKEN"] = "secret-value"

	if v := cp.ResolveSecret("${MY_TOKEN}"); v != "secret-value" {
		t.Errorf("got %q, want %q", v, "secret-value")
	}
}

func TestResolveSecret_MissingSecret(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	if v := cp.ResolveSecret("${NONEXISTENT}"); v != "" {
		t.Errorf("got %q, want empty", v)
	}
}

func TestResolveSecret_EmptyString(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	if v := cp.ResolveSecret(""); v != "" {
		t.Errorf("got %q, want empty", v)
	}
}

func TestResolveSecret_PartialSyntax(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	// Missing closing brace — not a reference.
	if v := cp.ResolveSecret("${UNCLOSED"); v != "${UNCLOSED" {
		t.Errorf("got %q", v)
	}

	// Missing opening — not a reference.
	if v := cp.ResolveSecret("NOCURLY}"); v != "NOCURLY}" {
		t.Errorf("got %q", v)
	}
}

func TestHasContent_WithChannels(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cp := &ControlPlane{
		config: Config{
			Channels: ChannelsConfig{
				Telegram: &TelegramChannelConfig{BotToken: "tok"},
			},
		},
		path:   path,
		logger: slog.Default(),
	}
	cp.config.initMaps()

	// hasContent should return true for channel-only config.
	if !cp.hasContent() {
		t.Error("expected hasContent() = true with channel config")
	}

	// Save should write the file.
	if err := cp.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not written: %v", err)
	}
}
