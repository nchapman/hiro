package cluster

import (
	"testing"
)

func TestClearRemote(t *testing.T) {
	t.Parallel()

	r := NewNodeRegistry()
	r.RegisterHome("leader")
	_ = r.Register("node-1", "Node 1", 0, "", "")
	_ = r.Register("node-2", "Node 2", 0, "", "")

	r.ClearRemote()

	if r.Len() != 1 {
		t.Fatalf("expected 1 node (home only), got %d", r.Len())
	}
	_, ok := r.Get(HomeNodeID)
	if !ok {
		t.Fatal("home node should still exist")
	}
	_, ok = r.Get("node-1")
	if ok {
		t.Fatal("node-1 should have been removed")
	}
}

func TestRegisterOverwrite(t *testing.T) {
	t.Parallel()

	r := NewNodeRegistry()
	_ = r.Register("node-1", "Node 1 v1", 2, "1.2.3.4:8080", ViaDirect)
	_ = r.Register("node-1", "Node 1 v2", 8, "5.6.7.8:9090", ViaRelay)

	node, ok := r.Get("node-1")
	if !ok {
		t.Fatal("expected node-1 to exist")
	}
	if node.Name != "Node 1 v2" {
		t.Fatalf("expected name %q, got %q", "Node 1 v2", node.Name)
	}
	if node.Capacity != 8 {
		t.Fatalf("expected capacity 8, got %d", node.Capacity)
	}
	if node.Via != ViaRelay {
		t.Fatalf("expected via %q, got %q", ViaRelay, node.Via)
	}
}

func TestSetOffline_Nonexistent(t *testing.T) {
	t.Parallel()

	r := NewNodeRegistry()
	// Should not panic.
	r.SetOffline("nonexistent")
}

func TestTouch_Nonexistent(t *testing.T) {
	t.Parallel()

	r := NewNodeRegistry()
	// Should not panic.
	r.Touch("nonexistent")
}

func TestIncrementActive_Nonexistent(t *testing.T) {
	t.Parallel()

	r := NewNodeRegistry()
	// Should not panic.
	r.IncrementActive("nonexistent")
}

func TestDecrementActive_Nonexistent(t *testing.T) {
	t.Parallel()

	r := NewNodeRegistry()
	// Should not panic.
	r.DecrementActive("nonexistent")
}

func TestNodeRegistry_RegisterWithViaAndAddr(t *testing.T) {
	t.Parallel()

	r := NewNodeRegistry()
	err := r.Register("node-1", "Node 1", 4, "10.0.0.1:50000", ViaRelay)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	node, ok := r.Get("node-1")
	if !ok {
		t.Fatal("node not found")
	}
	if node.Addr != "10.0.0.1:50000" {
		t.Fatalf("Addr = %q, want %q", node.Addr, "10.0.0.1:50000")
	}
	if node.Via != ViaRelay {
		t.Fatalf("Via = %q, want %q", node.Via, ViaRelay)
	}
}
