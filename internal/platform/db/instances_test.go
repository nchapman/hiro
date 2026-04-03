package db

import (
	"context"
	"errors"
	"testing"
)

func TestInstanceCRUD(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Create a root instance.
	err := d.CreateInstance(ctx, Instance{
		ID: "inst-1", AgentName: "coordinator", Mode: "persistent",
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// Get it back.
	inst, err := d.GetInstance(ctx, "inst-1")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if inst.AgentName != "coordinator" || inst.Mode != "persistent" || inst.Status != "running" {
		t.Errorf("unexpected instance: %+v", inst)
	}
	if inst.StoppedAt != nil {
		t.Error("expected nil StoppedAt for running instance")
	}

	// Get non-existent.
	_, err = d.GetInstance(ctx, "no-such-id")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Create child.
	err = d.CreateInstance(ctx, Instance{
		ID: "inst-2", AgentName: "worker", Mode: "ephemeral", ParentID: "inst-1",
	})
	if err != nil {
		t.Fatalf("CreateInstance child: %v", err)
	}
	child, _ := d.GetInstance(ctx, "inst-2")
	if child.ParentID != "inst-1" {
		t.Errorf("expected parent_id=inst-1, got %s", child.ParentID)
	}

	// Duplicate.
	err = d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "x", Mode: "ephemeral"})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}
}

func TestListInstances(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	d.CreateInstance(ctx, Instance{ID: "root", AgentName: "coord", Mode: "persistent"})
	d.CreateInstance(ctx, Instance{ID: "child-1", AgentName: "w1", Mode: "ephemeral", ParentID: "root"})
	d.CreateInstance(ctx, Instance{ID: "child-2", AgentName: "w2", Mode: "persistent", ParentID: "root"})

	// List all.
	all, err := d.ListInstances(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 instances, got %d", len(all))
	}

	// List by parent.
	children, err := d.ListChildInstances(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}

	// List by status.
	running, err := d.ListInstances(ctx, "", "running")
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 3 {
		t.Errorf("expected 3 running, got %d", len(running))
	}
}

func TestUpdateInstanceStatus(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "worker", Mode: "ephemeral"})

	// Stop it.
	if err := d.UpdateInstanceStatus(ctx, "inst-1", "stopped"); err != nil {
		t.Fatal(err)
	}
	inst, _ := d.GetInstance(ctx, "inst-1")
	if inst.Status != "stopped" {
		t.Errorf("expected stopped, got %s", inst.Status)
	}
	if inst.StoppedAt == nil {
		t.Error("expected StoppedAt to be set")
	}

	// Non-existent.
	err := d.UpdateInstanceStatus(ctx, "no-such", "stopped")
	if err == nil {
		t.Error("expected error for non-existent instance")
	}
}

func TestDeleteInstance(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "worker", Mode: "ephemeral"})

	if err := d.DeleteInstance(ctx, "inst-1"); err != nil {
		t.Fatal(err)
	}

	_, err := d.GetInstance(ctx, "inst-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Non-existent.
	err = d.DeleteInstance(ctx, "no-such")
	if err == nil {
		t.Error("expected error for non-existent instance")
	}
}

func TestInstanceConfig(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "worker", Mode: "persistent"})

	// Default config is empty.
	cfg, err := d.GetInstanceConfig(ctx, "inst-1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelOverride != "" || cfg.ReasoningEffort != "" {
		t.Errorf("expected empty config, got %+v", cfg)
	}

	// Update config.
	err = d.UpdateInstanceConfig(ctx, "inst-1", InstanceConfig{
		ModelOverride:   "claude-3-opus",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg, _ = d.GetInstanceConfig(ctx, "inst-1")
	if cfg.ModelOverride != "claude-3-opus" || cfg.ReasoningEffort != "high" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	// Non-existent.
	_, err = d.GetInstanceConfig(ctx, "no-such")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	err = d.UpdateInstanceConfig(ctx, "no-such", InstanceConfig{})
	if err == nil {
		t.Error("expected error for non-existent instance")
	}
}

func TestDeleteInstance_CascadesSessions(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateInstance(ctx, Instance{ID: "inst-1", AgentName: "worker", Mode: "persistent"})

	// Create a session under this instance.
	d.CreateSession(ctx, Session{
		ID: "sess-1", AgentName: "worker", Mode: "persistent", InstanceID: "inst-1",
	})

	// Delete the instance — session should cascade.
	d.DeleteInstance(ctx, "inst-1")

	_, err := d.GetSession(ctx, "sess-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected session to be cascade-deleted, got %v", err)
	}
}
