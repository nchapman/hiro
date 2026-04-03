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
