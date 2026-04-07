package db

import (
	"context"
	"errors"
	"testing"
)

func TestCreateSession_Duplicate(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	s := Session{ID: "dup-1", AgentName: "test", Mode: "persistent"}
	if err := d.CreateSession(ctx, s); err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	err := d.CreateSession(ctx, s)
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}
}

func TestCreateSession_WithInstanceID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-X", AgentName: "test", Mode: "persistent"})
	err := d.CreateSession(ctx, Session{
		ID: "sess-with-inst", AgentName: "test", Mode: "persistent", InstanceID: "inst-X",
	})
	if err != nil {
		t.Fatalf("CreateSession with instance: %v", err)
	}

	s, err := d.GetSession(ctx, "sess-with-inst")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.InstanceID != "inst-X" {
		t.Errorf("expected InstanceID=inst-X, got %q", s.InstanceID)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.GetSession(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListSessions_StatusFilter(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateSession(ctx, Session{ID: "s1", AgentName: "a", Mode: "persistent"})
	d.CreateSession(ctx, Session{ID: "s2", AgentName: "b", Mode: "persistent"})
	d.UpdateSessionStatus(ctx, "s1", "stopped")

	running, err := d.ListSessions(ctx, "", "running")
	if err != nil {
		t.Fatalf("ListSessions running: %v", err)
	}
	if len(running) != 1 || running[0].ID != "s2" {
		t.Errorf("expected 1 running session (s2), got %+v", running)
	}

	stopped, err := d.ListSessions(ctx, "", "stopped")
	if err != nil {
		t.Fatalf("ListSessions stopped: %v", err)
	}
	if len(stopped) != 1 || stopped[0].ID != "s1" {
		t.Errorf("expected 1 stopped session (s1), got %+v", stopped)
	}
}

func TestListSessions_NoFilters(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateSession(ctx, Session{ID: "s1", AgentName: "a", Mode: "persistent"})
	d.CreateSession(ctx, Session{ID: "s2", AgentName: "b", Mode: "ephemeral"})

	all, err := d.ListSessions(ctx, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(all))
	}
}

func TestListSessionsByInstance(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "test", Mode: "persistent"})
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent", InstanceID: "inst-1"})
	d.CreateSession(ctx, Session{ID: "s2", AgentName: "test", Mode: "persistent", InstanceID: "inst-1"})
	d.CreateSession(ctx, Session{ID: "s3", AgentName: "other", Mode: "persistent"})

	sessions, err := d.ListSessionsByInstance(ctx, "inst-1")
	if err != nil {
		t.Fatalf("ListSessionsByInstance: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions for inst-1, got %d", len(sessions))
	}
	for _, s := range sessions {
		if s.InstanceID != "inst-1" {
			t.Errorf("expected InstanceID=inst-1, got %q", s.InstanceID)
		}
	}
}

func TestUpdateSessionStatus_NotFound(t *testing.T) {
	d := openTestDB(t)
	err := d.UpdateSessionStatus(context.Background(), "nonexistent", "stopped")
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

func TestUpdateSessionStatus_Running(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateSession(ctx, Session{ID: "s1", AgentName: "a", Mode: "persistent"})
	d.UpdateSessionStatus(ctx, "s1", "stopped")

	// Update back to running — stopped_at should be cleared.
	if err := d.UpdateSessionStatus(ctx, "s1", "running"); err != nil {
		t.Fatalf("UpdateSessionStatus to running: %v", err)
	}
	s, _ := d.GetSession(ctx, "s1")
	if s.Status != "running" {
		t.Errorf("expected running, got %s", s.Status)
	}
	if s.StoppedAt != nil {
		t.Error("expected StoppedAt to be nil after resuming")
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	d := openTestDB(t)
	err := d.DeleteSession(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

func TestLatestSessionByChannel_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "test", Mode: "persistent"})

	_, ok, err := d.LatestSessionByChannel(ctx, "inst-1", "telegram", "12345")
	if err != nil {
		t.Fatalf("LatestSessionByChannel: %v", err)
	}
	if ok {
		t.Error("expected ok=false for instance with no channel sessions")
	}
}

func TestLatestSessionByChannel_ReturnsLatest(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "test", Mode: "persistent"})

	// Insert two sessions for the same instance+channel with explicit timestamps.
	d.db.Exec(`INSERT INTO sessions (id, instance_id, agent_name, mode, status, channel_type, channel_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"sess-old", "inst-1", "test", "persistent", "stopped", "telegram", "12345", "2026-01-01 00:00:00")
	d.db.Exec(`INSERT INTO sessions (id, instance_id, agent_name, mode, status, channel_type, channel_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"sess-new", "inst-1", "test", "persistent", "running", "telegram", "12345", "2026-01-02 00:00:00")

	sess, ok, err := d.LatestSessionByChannel(ctx, "inst-1", "telegram", "12345")
	if err != nil {
		t.Fatalf("LatestSessionByChannel: %v", err)
	}
	if !ok {
		t.Fatal("expected to find a session")
	}
	if sess.ID != "sess-new" {
		t.Errorf("expected sess-new, got %s", sess.ID)
	}
	if sess.InstanceID != "inst-1" {
		t.Errorf("expected InstanceID=inst-1, got %s", sess.InstanceID)
	}
	if sess.ChannelType != "telegram" {
		t.Errorf("expected ChannelType=telegram, got %s", sess.ChannelType)
	}
	if sess.ChannelID != "12345" {
		t.Errorf("expected ChannelID=12345, got %s", sess.ChannelID)
	}
}

func TestLatestSessionByChannel_DifferentChannels(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "test", Mode: "persistent"})

	// One session via the web channel (empty channel_id) and one via telegram.
	d.db.Exec(`INSERT INTO sessions (id, instance_id, agent_name, mode, status, channel_type, channel_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"sess-web", "inst-1", "test", "persistent", "running", "web", "", "2026-01-01 00:00:00")
	d.db.Exec(`INSERT INTO sessions (id, instance_id, agent_name, mode, status, channel_type, channel_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"sess-tg", "inst-1", "test", "persistent", "running", "telegram", "12345", "2026-01-01 00:00:00")

	// Querying for web should not return the telegram session.
	webSess, ok, err := d.LatestSessionByChannel(ctx, "inst-1", "web", "")
	if err != nil {
		t.Fatalf("LatestSessionByChannel web: %v", err)
	}
	if !ok {
		t.Fatal("expected to find web session")
	}
	if webSess.ID != "sess-web" {
		t.Errorf("expected sess-web, got %s", webSess.ID)
	}

	// Querying for telegram should not return the web session.
	tgSess, ok, err := d.LatestSessionByChannel(ctx, "inst-1", "telegram", "12345")
	if err != nil {
		t.Fatalf("LatestSessionByChannel telegram: %v", err)
	}
	if !ok {
		t.Fatal("expected to find telegram session")
	}
	if tgSess.ID != "sess-tg" {
		t.Errorf("expected sess-tg, got %s", tgSess.ID)
	}

	// Querying for a channel with no sessions returns not found.
	_, ok, err = d.LatestSessionByChannel(ctx, "inst-1", "slack", "C999")
	if err != nil {
		t.Fatalf("LatestSessionByChannel slack: %v", err)
	}
	if ok {
		t.Error("expected ok=false for slack channel with no sessions")
	}
}

func TestListSessionsByChannelType(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "test", Mode: "persistent"})

	// Create sessions with different channel types.
	if err := d.CreateSession(ctx, Session{
		ID: "s-web-1", InstanceID: "inst-1", AgentName: "test", Mode: "persistent",
		ChannelType: "web", ChannelID: "",
	}); err != nil {
		t.Fatalf("CreateSession web: %v", err)
	}
	if err := d.CreateSession(ctx, Session{
		ID: "s-tg-1", InstanceID: "inst-1", AgentName: "test", Mode: "persistent",
		ChannelType: "telegram", ChannelID: "111",
	}); err != nil {
		t.Fatalf("CreateSession telegram 1: %v", err)
	}
	if err := d.CreateSession(ctx, Session{
		ID: "s-tg-2", InstanceID: "inst-1", AgentName: "test", Mode: "persistent",
		ChannelType: "telegram", ChannelID: "222",
	}); err != nil {
		t.Fatalf("CreateSession telegram 2: %v", err)
	}

	// Filtering by "telegram" returns only those two sessions.
	tgSessions, err := d.ListSessionsByChannelType(ctx, "inst-1", "telegram")
	if err != nil {
		t.Fatalf("ListSessionsByChannelType telegram: %v", err)
	}
	if len(tgSessions) != 2 {
		t.Fatalf("expected 2 telegram sessions, got %d", len(tgSessions))
	}
	for _, s := range tgSessions {
		if s.ChannelType != "telegram" {
			t.Errorf("expected ChannelType=telegram, got %s", s.ChannelType)
		}
	}

	// Filtering by "web" returns only the one web session.
	webSessions, err := d.ListSessionsByChannelType(ctx, "inst-1", "web")
	if err != nil {
		t.Fatalf("ListSessionsByChannelType web: %v", err)
	}
	if len(webSessions) != 1 || webSessions[0].ID != "s-web-1" {
		t.Errorf("expected 1 web session (s-web-1), got %+v", webSessions)
	}

	// Filtering by a type with no sessions returns an empty slice, not an error.
	slackSessions, err := d.ListSessionsByChannelType(ctx, "inst-1", "slack")
	if err != nil {
		t.Fatalf("ListSessionsByChannelType slack: %v", err)
	}
	if len(slackSessions) != 0 {
		t.Errorf("expected 0 slack sessions, got %d", len(slackSessions))
	}
}

func TestCreateSession_ChannelColumns(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "test", Mode: "persistent"})

	if err := d.CreateSession(ctx, Session{
		ID:          "sess-ch",
		InstanceID:  "inst-1",
		AgentName:   "test",
		Mode:        "persistent",
		ChannelType: "slack",
		ChannelID:   "C1234567890",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	s, err := d.GetSession(ctx, "sess-ch")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.ChannelType != "slack" {
		t.Errorf("expected ChannelType=slack, got %q", s.ChannelType)
	}
	if s.ChannelID != "C1234567890" {
		t.Errorf("expected ChannelID=C1234567890, got %q", s.ChannelID)
	}
	if s.InstanceID != "inst-1" {
		t.Errorf("expected InstanceID=inst-1, got %q", s.InstanceID)
	}
	if s.Status != "running" {
		t.Errorf("expected Status=running, got %q", s.Status)
	}
}
