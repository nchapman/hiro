package cluster

import (
	"testing"
)

func TestNewNodeRegistry(t *testing.T) {
	r := NewNodeRegistry()
	if r.Len() != 0 {
		t.Fatalf("expected empty registry, got %d nodes", r.Len())
	}
}

func TestRegisterHome(t *testing.T) {
	r := NewNodeRegistry()
	r.RegisterHome("my-leader")

	node, ok := r.Get(HomeNodeID)
	if !ok {
		t.Fatal("home node not found")
	}
	if node.Name != "my-leader" {
		t.Errorf("expected name %q, got %q", "my-leader", node.Name)
	}
	if !node.IsHome {
		t.Error("expected IsHome to be true")
	}
	if node.Status != NodeOnline {
		t.Errorf("expected status %q, got %q", NodeOnline, node.Status)
	}
	if r.Len() != 1 {
		t.Errorf("expected 1 node, got %d", r.Len())
	}
}

func TestRegisterRemoteNode(t *testing.T) {
	r := NewNodeRegistry()
	err := r.Register("gpu-box", "GPU Box", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	node, ok := r.Get("gpu-box")
	if !ok {
		t.Fatal("node not found")
	}
	if node.Name != "GPU Box" {
		t.Errorf("expected name %q, got %q", "GPU Box", node.Name)
	}
	if node.IsHome {
		t.Error("expected IsHome to be false")
	}
	if node.Capacity != 4 {
		t.Errorf("expected capacity 4, got %d", node.Capacity)
	}
}

func TestRegisterReservedHomeID(t *testing.T) {
	r := NewNodeRegistry()
	err := r.Register(HomeNodeID, "impostor", 0)
	if err == nil {
		t.Fatal("expected error when registering with home ID")
	}
}

func TestUnregister(t *testing.T) {
	r := NewNodeRegistry()
	_ = r.Register("node-1", "Node 1", 0)
	r.Unregister("node-1")

	_, ok := r.Get("node-1")
	if ok {
		t.Error("expected node to be removed")
	}
	if r.Len() != 0 {
		t.Errorf("expected 0 nodes, got %d", r.Len())
	}
}

func TestSetOffline(t *testing.T) {
	r := NewNodeRegistry()
	_ = r.Register("node-1", "Node 1", 0)
	r.SetOffline("node-1")

	node, _ := r.Get("node-1")
	if node.Status != NodeOffline {
		t.Errorf("expected status %q, got %q", NodeOffline, node.Status)
	}
}

func TestTouch(t *testing.T) {
	r := NewNodeRegistry()
	_ = r.Register("node-1", "Node 1", 0)
	node1, _ := r.Get("node-1")
	firstSeen := node1.LastSeen

	r.Touch("node-1")
	node2, _ := r.Get("node-1")
	if node2.LastSeen.Before(firstSeen) {
		t.Error("expected LastSeen to advance")
	}
}

func TestListOrder(t *testing.T) {
	r := NewNodeRegistry()
	_ = r.Register("beta", "Beta", 0)
	r.RegisterHome("leader")
	_ = r.Register("alpha", "Alpha", 0)

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(list))
	}
	if list[0].ID != HomeNodeID {
		t.Errorf("expected home node first, got %q", list[0].ID)
	}
}

func TestOnlineNodes(t *testing.T) {
	r := NewNodeRegistry()
	r.RegisterHome("leader")
	_ = r.Register("node-1", "Node 1", 0)
	_ = r.Register("node-2", "Node 2", 0)
	r.SetOffline("node-2")

	online := r.OnlineNodes()
	if len(online) != 2 {
		t.Errorf("expected 2 online nodes, got %d", len(online))
	}
}

func TestActiveCount(t *testing.T) {
	r := NewNodeRegistry()
	_ = r.Register("node-1", "Node 1", 0)

	r.IncrementActive("node-1")
	r.IncrementActive("node-1")
	node, _ := r.Get("node-1")
	if node.ActiveCount != 2 {
		t.Errorf("expected active count 2, got %d", node.ActiveCount)
	}

	r.DecrementActive("node-1")
	node, _ = r.Get("node-1")
	if node.ActiveCount != 1 {
		t.Errorf("expected active count 1, got %d", node.ActiveCount)
	}

	// Decrement below zero should clamp at 0.
	r.DecrementActive("node-1")
	r.DecrementActive("node-1")
	node, _ = r.Get("node-1")
	if node.ActiveCount != 0 {
		t.Errorf("expected active count 0, got %d", node.ActiveCount)
	}
}

func TestGetNotFound(t *testing.T) {
	r := NewNodeRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}
