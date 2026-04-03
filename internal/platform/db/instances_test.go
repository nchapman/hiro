package db

import (
	"errors"
	"testing"
)

func TestInstanceCRUD(t *testing.T) {
	d := openTestDB(t)

	// Create a root instance.
	err := d.CreateInstance(Instance{
		ID: "inst-1", AgentName: "coordinator", Mode: "persistent",
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// Get it back.
	inst, err := d.GetInstance("inst-1")
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
	_, err = d.GetInstance("no-such-id")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Create child.
	err = d.CreateInstance(Instance{
		ID: "inst-2", AgentName: "worker", Mode: "ephemeral", ParentID: "inst-1",
	})
	if err != nil {
		t.Fatalf("CreateInstance child: %v", err)
	}
	child, _ := d.GetInstance("inst-2")
	if child.ParentID != "inst-1" {
		t.Errorf("expected parent_id=inst-1, got %s", child.ParentID)
	}

	// Duplicate.
	err = d.CreateInstance(Instance{ID: "inst-1", AgentName: "x", Mode: "ephemeral"})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}
}

func TestListInstances(t *testing.T) {
	d := openTestDB(t)

	d.CreateInstance(Instance{ID: "root", AgentName: "coord", Mode: "persistent"})
	d.CreateInstance(Instance{ID: "child-1", AgentName: "w1", Mode: "ephemeral", ParentID: "root"})
	d.CreateInstance(Instance{ID: "child-2", AgentName: "w2", Mode: "persistent", ParentID: "root"})

	// List all.
	all, err := d.ListInstances("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 instances, got %d", len(all))
	}

	// List by parent.
	children, err := d.ListChildInstances("root")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}

	// List by status.
	running, err := d.ListInstances("", "running")
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 3 {
		t.Errorf("expected 3 running, got %d", len(running))
	}
}

func TestUpdateInstanceStatus(t *testing.T) {
	d := openTestDB(t)
	d.CreateInstance(Instance{ID: "inst-1", AgentName: "worker", Mode: "ephemeral"})

	// Stop it.
	if err := d.UpdateInstanceStatus("inst-1", "stopped"); err != nil {
		t.Fatal(err)
	}
	inst, _ := d.GetInstance("inst-1")
	if inst.Status != "stopped" {
		t.Errorf("expected stopped, got %s", inst.Status)
	}
	if inst.StoppedAt == nil {
		t.Error("expected StoppedAt to be set")
	}

	// Non-existent.
	err := d.UpdateInstanceStatus("no-such", "stopped")
	if err == nil {
		t.Error("expected error for non-existent instance")
	}
}

func TestDeleteInstance(t *testing.T) {
	d := openTestDB(t)
	d.CreateInstance(Instance{ID: "inst-1", AgentName: "worker", Mode: "ephemeral"})

	if err := d.DeleteInstance("inst-1"); err != nil {
		t.Fatal(err)
	}

	_, err := d.GetInstance("inst-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Non-existent.
	err = d.DeleteInstance("no-such")
	if err == nil {
		t.Error("expected error for non-existent instance")
	}
}

func TestInstanceConfig(t *testing.T) {
	d := openTestDB(t)
	d.CreateInstance(Instance{ID: "inst-1", AgentName: "worker", Mode: "persistent"})

	// Default config is empty.
	cfg, err := d.GetInstanceConfig("inst-1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelOverride != "" || cfg.ReasoningEffort != "" {
		t.Errorf("expected empty config, got %+v", cfg)
	}

	// Update config.
	err = d.UpdateInstanceConfig("inst-1", InstanceConfig{
		ModelOverride:   "claude-3-opus",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg, _ = d.GetInstanceConfig("inst-1")
	if cfg.ModelOverride != "claude-3-opus" || cfg.ReasoningEffort != "high" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	// Non-existent.
	_, err = d.GetInstanceConfig("no-such")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	err = d.UpdateInstanceConfig("no-such", InstanceConfig{})
	if err == nil {
		t.Error("expected error for non-existent instance")
	}
}

func TestDeleteInstance_CascadesSessions(t *testing.T) {
	d := openTestDB(t)
	d.CreateInstance(Instance{ID: "inst-1", AgentName: "worker", Mode: "persistent"})

	// Create a session under this instance.
	d.CreateSession(Session{
		ID: "sess-1", AgentName: "worker", Mode: "persistent", InstanceID: "inst-1",
	})

	// Delete the instance — session should cascade.
	d.DeleteInstance("inst-1")

	_, err := d.GetSession("sess-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected session to be cascade-deleted, got %v", err)
	}
}
