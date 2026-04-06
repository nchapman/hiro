package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubscriptionCRUD(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Create parent instance (required by FK).
	if err := d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "operator", Mode: "persistent"}); err != nil {
		t.Fatal(err)
	}

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub := Subscription{
		ID:         "sub-1",
		InstanceID: "inst-1",
		Name:       "daily-report",
		Trigger:    TriggerDef{Type: "cron", Expr: "0 9 * * *"},
		Message:    "Generate daily report",
		Status:     "active",
		NextFire:   &nextFire,
	}

	// Create.
	if err := d.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// Get by ID.
	got, err := d.GetSubscription(ctx, "sub-1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.Name != "daily-report" || got.Trigger.Expr != "0 9 * * *" || got.Status != "active" {
		t.Errorf("unexpected: %+v", got)
	}
	if got.NextFire == nil {
		t.Fatal("expected non-nil NextFire")
	}

	// Get by name.
	got2, err := d.GetSubscriptionByName(ctx, "inst-1", "daily-report")
	if err != nil {
		t.Fatalf("GetSubscriptionByName: %v", err)
	}
	if got2.ID != "sub-1" {
		t.Errorf("expected sub-1, got %s", got2.ID)
	}

	// Duplicate name.
	err = d.CreateSubscription(ctx, Subscription{
		ID: "sub-2", InstanceID: "inst-1", Name: "daily-report",
		Trigger: TriggerDef{Type: "cron", Expr: "0 10 * * *"},
		Message: "x", Status: "active",
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}

	// List by instance.
	list, err := d.ListSubscriptionsByInstance(ctx, "inst-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "sub-1" {
		t.Errorf("unexpected list: %+v", list)
	}

	// List active.
	active, err := d.ListActiveSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active, got %d", len(active))
	}

	// Update fired.
	newNext := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := d.UpdateSubscriptionFired(ctx, "sub-1", time.Now().UTC(), &newNext); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetSubscription(ctx, "sub-1")
	if got.FireCount != 1 || got.ErrorCount != 0 || got.LastFired == nil {
		t.Errorf("unexpected after fire: fires=%d errors=%d lastFired=%v", got.FireCount, got.ErrorCount, got.LastFired)
	}

	// Update error.
	if err := d.UpdateSubscriptionError(ctx, "sub-1", &newNext, "test error"); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetSubscription(ctx, "sub-1")
	if got.ErrorCount != 1 || got.LastError != "test error" {
		t.Errorf("unexpected after error: errors=%d lastErr=%s", got.ErrorCount, got.LastError)
	}

	// Delete.
	if err := d.DeleteSubscription(ctx, "sub-1"); err != nil {
		t.Fatal(err)
	}
	_, err = d.GetSubscription(ctx, "sub-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestSubscriptionPauseResume(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "op", Mode: "persistent"})

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-a", InstanceID: "inst-1", Name: "a",
		Trigger: TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message: "a", Status: "active", NextFire: &nextFire,
	})
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-b", InstanceID: "inst-1", Name: "b",
		Trigger: TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message: "b", Status: "active", NextFire: &nextFire,
	})

	// Pause all.
	if err := d.PauseInstanceSubscriptions(ctx, "inst-1"); err != nil {
		t.Fatal(err)
	}
	active, _ := d.ListActiveSubscriptions(ctx)
	if len(active) != 0 {
		t.Errorf("expected 0 active after pause, got %d", len(active))
	}

	// Resume.
	resumed, err := d.ResumeInstanceSubscriptions(ctx, "inst-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 2 {
		t.Errorf("expected 2 resumed, got %d", len(resumed))
	}
}

func TestSubscriptionCascadeDelete(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "op", Mode: "persistent"})
	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-1", InstanceID: "inst-1", Name: "x",
		Trigger: TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message: "x", Status: "active", NextFire: &nextFire,
	})

	// Deleting the instance should cascade to subscriptions.
	if err := d.DeleteInstance(ctx, "inst-1"); err != nil {
		t.Fatal(err)
	}
	_, err := d.GetSubscription(ctx, "sub-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after cascade delete, got %v", err)
	}
}

func TestUpdateSubscriptionStatus(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "op", Mode: "persistent"})
	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-1", InstanceID: "inst-1", Name: "status-test",
		Trigger: TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message: "x", Status: "active", NextFire: &nextFire,
	})

	// Update status only (no next_fire change).
	if err := d.UpdateSubscriptionStatus(ctx, "sub-1", "paused", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetSubscription(ctx, "sub-1")
	if got.Status != "paused" {
		t.Errorf("expected 'paused', got %q", got.Status)
	}

	// Update status with a new next_fire.
	newFire := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := d.UpdateSubscriptionStatus(ctx, "sub-1", "active", &newFire); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetSubscription(ctx, "sub-1")
	if got.Status != "active" {
		t.Errorf("expected 'active', got %q", got.Status)
	}
	if got.NextFire == nil {
		t.Fatal("expected non-nil next_fire")
	}

	// Clear next_fire by passing zero time pointer.
	zero := time.Time{}
	if err := d.UpdateSubscriptionStatus(ctx, "sub-1", "paused", &zero); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetSubscription(ctx, "sub-1")
	if got.NextFire != nil {
		t.Error("expected nil next_fire after clearing with zero time")
	}

	// Non-existent subscription.
	err := d.UpdateSubscriptionStatus(ctx, "no-such-id", "active", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListAllSubscriptions(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Empty list.
	all, err := d.ListAllSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0, got %d", len(all))
	}

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "op", Mode: "persistent"})
	d.CreateInstance(ctx, Instance{ID: "inst-2", AgentName: "op", Mode: "persistent"})

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-a", InstanceID: "inst-1", Name: "a",
		Trigger: TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message: "a", Status: "active", NextFire: &nextFire,
	})
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-b", InstanceID: "inst-2", Name: "b",
		Trigger: TriggerDef{Type: "cron", Expr: "0 9 * * *"},
		Message: "b", Status: "paused", NextFire: &nextFire,
	})

	all, err = d.ListAllSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2, got %d", len(all))
	}
	// Ordered by instance_id then created_at.
	if all[0].InstanceID != "inst-1" || all[1].InstanceID != "inst-2" {
		t.Errorf("unexpected order: %s, %s", all[0].InstanceID, all[1].InstanceID)
	}
}

func TestDeleteSubscriptionByName(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "op", Mode: "persistent"})
	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-1", InstanceID: "inst-1", Name: "to-delete",
		Trigger: TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message: "x", Status: "active", NextFire: &nextFire,
	})

	if err := d.DeleteSubscriptionByName(ctx, "inst-1", "to-delete"); err != nil {
		t.Fatal(err)
	}

	// Should be gone.
	_, err := d.GetSubscription(ctx, "sub-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Deleting again should return not found.
	err = d.DeleteSubscriptionByName(ctx, "inst-1", "to-delete")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on second delete, got %v", err)
	}
}

func TestSessionBySubscription(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "op", Mode: "persistent"})
	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	d.CreateSubscription(ctx, Subscription{
		ID: "sub-1", InstanceID: "inst-1", Name: "x",
		Trigger: TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message: "x", Status: "active", NextFire: &nextFire,
	})

	// No session yet.
	_, found, err := d.SessionBySubscription(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected no session for subscription")
	}

	// Create session linked to subscription.
	d.CreateSession(ctx, Session{
		ID: "sess-1", InstanceID: "inst-1", AgentName: "op",
		Mode: "persistent", SubscriptionID: "sub-1",
	})

	sess, found, err := d.SessionBySubscription(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if !found || sess.ID != "sess-1" {
		t.Errorf("expected sess-1, got %+v", sess)
	}
}
