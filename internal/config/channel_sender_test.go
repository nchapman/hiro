package config

import (
	"testing"
	"time"
)

func TestSenderStatus_NotFound(t *testing.T) {
	c := &InstanceChannelsConfig{}
	_, found := c.SenderStatus("tg:999")
	if found {
		t.Error("expected not found")
	}
}

func TestSetSender_AddNew(t *testing.T) {
	c := &InstanceChannelsConfig{}
	c.SetSender("tg:100", ChannelAccessPending, "Test", "hello")

	if len(c.Senders) != 1 {
		t.Fatalf("expected 1 sender, got %d", len(c.Senders))
	}
	s := c.Senders[0]
	if s.Key != "tg:100" || s.Status != ChannelAccessPending || s.DisplayName != "Test" || s.SampleText != "hello" {
		t.Errorf("unexpected sender: %+v", s)
	}
	if s.FirstSeen.IsZero() || s.LastSeen.IsZero() {
		t.Error("timestamps should be set")
	}
}

func TestSetSender_UpdateExisting(t *testing.T) {
	c := &InstanceChannelsConfig{}
	c.SetSender("tg:100", ChannelAccessPending, "Original Name", "first msg")
	c.SetSender("tg:100", ChannelAccessApproved, "", "") // empty name/sample should preserve

	if len(c.Senders) != 1 {
		t.Fatalf("expected 1 sender, got %d", len(c.Senders))
	}
	s := c.Senders[0]
	if s.Status != ChannelAccessApproved {
		t.Errorf("status=%v, want approved", s.Status)
	}
	if s.DisplayName != "Original Name" {
		t.Errorf("display_name=%q, want preserved original", s.DisplayName)
	}
	if s.SampleText != "first msg" {
		t.Errorf("sample_text=%q, want preserved original", s.SampleText)
	}
}

func TestSendersByStatus(t *testing.T) {
	c := &InstanceChannelsConfig{}
	c.SetSender("tg:1", ChannelAccessPending, "A", "")
	c.SetSender("tg:2", ChannelAccessApproved, "B", "")
	c.SetSender("tg:3", ChannelAccessPending, "C", "")
	c.SetSender("tg:4", ChannelAccessBlocked, "D", "")

	pending := c.SendersByStatus(ChannelAccessPending)
	if len(pending) != 2 {
		t.Errorf("expected 2 pending, got %d", len(pending))
	}

	approved := c.SendersByStatus(ChannelAccessApproved)
	if len(approved) != 1 {
		t.Errorf("expected 1 approved, got %d", len(approved))
	}

	blocked := c.SendersByStatus(ChannelAccessBlocked)
	if len(blocked) != 1 {
		t.Errorf("expected 1 blocked, got %d", len(blocked))
	}
}

func TestRemoveSender(t *testing.T) {
	c := &InstanceChannelsConfig{}
	c.SetSender("tg:100", ChannelAccessPending, "Test", "")
	c.SetSender("tg:200", ChannelAccessApproved, "Other", "")

	if !c.RemoveSender("tg:100") {
		t.Error("expected RemoveSender to return true")
	}
	if len(c.Senders) != 1 {
		t.Fatalf("expected 1 sender remaining, got %d", len(c.Senders))
	}
	if c.Senders[0].Key != "tg:200" {
		t.Errorf("wrong sender remaining: %s", c.Senders[0].Key)
	}
}

func TestRemoveSender_NotFound(t *testing.T) {
	c := &InstanceChannelsConfig{}
	if c.RemoveSender("tg:999") {
		t.Error("expected RemoveSender to return false for missing key")
	}
}

func TestTouchSender(t *testing.T) {
	c := &InstanceChannelsConfig{}
	c.SetSender("tg:100", ChannelAccessPending, "Test", "")
	originalLastSeen := c.Senders[0].LastSeen

	time.Sleep(time.Millisecond)
	c.TouchSender("tg:100")
	if !c.Senders[0].LastSeen.After(originalLastSeen) {
		t.Error("LastSeen should have been updated to a later time")
	}
}

func TestSaveAndLoadSenders(t *testing.T) {
	dir := t.TempDir()
	cfg := InstanceConfig{
		Channels: &InstanceChannelsConfig{
			Telegram: &InstanceTelegramConfig{BotToken: "test"},
		},
	}
	cfg.Channels.SetSender("tg:100", ChannelAccessApproved, "User A", "hello")
	cfg.Channels.SetSender("slack:C123", ChannelAccessPending, "Channel", "hi")

	if err := SaveInstanceConfig(dir, cfg); err != nil {
		t.Fatalf("SaveInstanceConfig: %v", err)
	}

	got, err := LoadInstanceConfig(dir)
	if err != nil {
		t.Fatalf("LoadInstanceConfig: %v", err)
	}
	if got.Channels == nil || len(got.Channels.Senders) != 2 {
		t.Fatalf("expected 2 senders, got %+v", got.Channels)
	}

	status, found := got.Channels.SenderStatus("tg:100")
	if !found || status != ChannelAccessApproved {
		t.Errorf("tg:100 status=%v found=%v", status, found)
	}

	status, found = got.Channels.SenderStatus("slack:C123")
	if !found || status != ChannelAccessPending {
		t.Errorf("slack:C123 status=%v found=%v", status, found)
	}
}
