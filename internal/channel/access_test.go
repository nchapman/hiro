package channel

import (
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nchapman/hiro/internal/config"
)

type fakeAccessManager struct {
	configPath string
}

func (f *fakeAccessManager) InstanceConfigPath(_ string) string { return f.configPath }

func TestConfigAccessChecker_UnknownSenderBecomesPending(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// Seed with empty channels config so the checker has something to write to.
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	result := ac.CheckAccess("inst-1", "tg:999", "Test User", "hello")
	if result != AccessPending {
		t.Fatalf("expected AccessPending, got %d", result)
	}

	// Verify it was persisted.
	cfg, _ = config.LoadInstanceConfig(configPath)
	status, found := cfg.Channels.SenderStatus("tg:999")
	if !found || status != config.ChannelAccessPending {
		t.Errorf("sender not persisted as pending: found=%v status=%v", found, status)
	}
}

func TestConfigAccessChecker_ApprovedSenderAllowed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	cfg.Channels.SetSender("tg:100", config.ChannelAccessApproved, "Approved User", "")
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	result := ac.CheckAccess("inst-1", "tg:100", "", "")
	if result != AccessAllow {
		t.Fatalf("expected AccessAllow, got %d", result)
	}
}

func TestConfigAccessChecker_BlockedSenderDenied(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	cfg.Channels.SetSender("tg:666", config.ChannelAccessBlocked, "Bad User", "")
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	result := ac.CheckAccess("inst-1", "tg:666", "", "")
	if result != AccessDeny {
		t.Fatalf("expected AccessDeny, got %d", result)
	}
}

func TestConfigAccessChecker_PendingSenderReturnsPending(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	cfg.Channels.SetSender("tg:888", config.ChannelAccessPending, "Pending User", "hi")
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	result := ac.CheckAccess("inst-1", "tg:888", "", "")
	if result != AccessPending {
		t.Fatalf("expected AccessPending, got %d", result)
	}
}

func TestConfigAccessChecker_UpdateSenderStatus(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	cfg.Channels.SetSender("tg:100", config.ChannelAccessPending, "User", "")
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	if err := ac.UpdateSenderStatus("inst-1", "tg:100", config.ChannelAccessApproved); err != nil {
		t.Fatalf("UpdateSenderStatus: %v", err)
	}

	// Verify persisted.
	cfg, _ = config.LoadInstanceConfig(configPath)
	status, _ := cfg.Channels.SenderStatus("tg:100")
	if status != config.ChannelAccessApproved {
		t.Errorf("expected approved, got %v", status)
	}
}

func TestConfigAccessChecker_UpdateSenderStatus_NotFound(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	err := ac.UpdateSenderStatus("inst-1", "tg:999", config.ChannelAccessApproved)
	if !errors.Is(err, ErrSenderNotFound) {
		t.Fatalf("expected ErrSenderNotFound, got %v", err)
	}
}

func TestConfigAccessChecker_RemoveSender(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	cfg.Channels.SetSender("tg:100", config.ChannelAccessPending, "User", "")
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	if err := ac.RemoveSender("inst-1", "tg:100"); err != nil {
		t.Fatalf("RemoveSender: %v", err)
	}

	// Verify removed.
	cfg, _ = config.LoadInstanceConfig(configPath)
	if _, found := cfg.Channels.SenderStatus("tg:100"); found {
		t.Error("sender should be removed")
	}
}

func TestConfigAccessChecker_RemoveSender_NotFound(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.InstanceConfig{Channels: &config.InstanceChannelsConfig{}}
	config.SaveInstanceConfig(configPath, cfg)

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	err := ac.RemoveSender("inst-1", "tg:999")
	if !errors.Is(err, ErrSenderNotFound) {
		t.Fatalf("expected ErrSenderNotFound, got %v", err)
	}
}

func TestConfigAccessChecker_NoChannelsConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// Empty config — no Channels field at all.
	config.SaveInstanceConfig(configPath, config.InstanceConfig{})

	ac := NewConfigAccessChecker(&fakeAccessManager{configPath: configPath}, slog.Default())

	// Should register as pending (creates Channels on the fly).
	result := ac.CheckAccess("inst-1", "tg:111", "User", "hello")
	if result != AccessPending {
		t.Fatalf("expected AccessPending, got %d", result)
	}

	cfg, _ := config.LoadInstanceConfig(configPath)
	if cfg.Channels == nil {
		t.Fatal("Channels should have been created")
	}
	if _, found := cfg.Channels.SenderStatus("tg:111"); !found {
		t.Error("sender should be persisted")
	}
}
