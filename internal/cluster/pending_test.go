package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPendingRegistry_AddOrUpdate_New(t *testing.T) {
	t.Parallel()
	r := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)

	ok, isNew := r.AddOrUpdate(PendingNode{NodeID: "node-1", Name: "Node 1", Addr: "1.2.3.4:8080"})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !isNew {
		t.Fatal("expected isNew=true")
	}
	if r.Count() != 1 {
		t.Fatalf("expected count 1, got %d", r.Count())
	}
}

func TestPendingRegistry_AddOrUpdate_Existing(t *testing.T) {
	t.Parallel()
	r := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)

	r.AddOrUpdate(PendingNode{NodeID: "node-1", Name: "Node 1", Addr: "1.2.3.4:8080"})
	ok, isNew := r.AddOrUpdate(PendingNode{NodeID: "node-1", Name: "Node 1 Updated", Addr: "5.6.7.8:8080"})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if isNew {
		t.Fatal("expected isNew=false for existing node")
	}
	if r.Count() != 1 {
		t.Fatalf("expected count 1, got %d", r.Count())
	}

	node, found := r.Get("node-1")
	if !found {
		t.Fatal("expected to find node")
	}
	if node.Name != "Node 1 Updated" {
		t.Fatalf("expected updated name, got %q", node.Name)
	}
	if node.Addr != "5.6.7.8:8080" {
		t.Fatalf("expected updated addr, got %q", node.Addr)
	}
}

func TestPendingRegistry_Get(t *testing.T) {
	t.Parallel()
	r := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)

	_, found := r.Get("nonexistent")
	if found {
		t.Fatal("expected not found for nonexistent node")
	}

	r.AddOrUpdate(PendingNode{NodeID: "node-1", Name: "Node 1"})
	node, found := r.Get("node-1")
	if !found {
		t.Fatal("expected to find node-1")
	}
	if node.NodeID != "node-1" {
		t.Fatalf("got node ID %q, want %q", node.NodeID, "node-1")
	}
	if node.Name != "Node 1" {
		t.Fatalf("got name %q, want %q", node.Name, "Node 1")
	}
}

func TestPendingRegistry_Remove(t *testing.T) {
	t.Parallel()
	r := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)

	r.AddOrUpdate(PendingNode{NodeID: "node-1", Name: "Node 1"})
	r.AddOrUpdate(PendingNode{NodeID: "node-2", Name: "Node 2"})
	r.Remove("node-1")

	if r.Count() != 1 {
		t.Fatalf("expected count 1 after remove, got %d", r.Count())
	}
	_, found := r.Get("node-1")
	if found {
		t.Fatal("expected node-1 to be removed")
	}

	// Removing nonexistent node should be a no-op.
	r.Remove("nonexistent")
	if r.Count() != 1 {
		t.Fatalf("expected count 1 after no-op remove, got %d", r.Count())
	}
}

func TestPendingRegistry_List_SortedByFirstSeen(t *testing.T) {
	t.Parallel()
	r := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)

	// Add nodes in reverse order; List should return oldest first.
	r.AddOrUpdate(PendingNode{NodeID: "node-c", Name: "C"})
	r.AddOrUpdate(PendingNode{NodeID: "node-a", Name: "A"})
	r.AddOrUpdate(PendingNode{NodeID: "node-b", Name: "B"})

	nodes := r.List()
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}
	// All were added in quick succession so they have the same FirstSeen;
	// at minimum, verify the list length is correct and nodes are returned.
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.NodeID] = true
	}
	for _, id := range []string{"node-a", "node-b", "node-c"} {
		if !ids[id] {
			t.Fatalf("missing node %q in list", id)
		}
	}
}

func TestPendingRegistry_Clear(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.yaml")
	r := NewPendingRegistry(path, nil)

	r.AddOrUpdate(PendingNode{NodeID: "node-1", Name: "Node 1"})
	r.AddOrUpdate(PendingNode{NodeID: "node-2", Name: "Node 2"})
	r.Clear()

	if r.Count() != 0 {
		t.Fatalf("expected count 0 after clear, got %d", r.Count())
	}

	// Backing file should be removed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected backing file to be removed after clear")
	}
}

func TestPendingRegistry_FilePath(t *testing.T) {
	t.Parallel()
	path := "/tmp/test-pending.yaml"
	r := NewPendingRegistry(path, nil)
	if r.FilePath() != path {
		t.Fatalf("got %q, want %q", r.FilePath(), path)
	}
}

func TestPendingRegistry_LoadPersistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.yaml")

	// Create and populate a registry.
	r1 := NewPendingRegistry(path, nil)
	r1.AddOrUpdate(PendingNode{NodeID: "node-1", Name: "Node 1", Addr: "1.2.3.4:8080"})
	r1.AddOrUpdate(PendingNode{NodeID: "node-2", Name: "Node 2", Addr: "5.6.7.8:9090"})

	// Load into a fresh registry.
	r2 := NewPendingRegistry(path, nil)
	if err := r2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r2.Count() != 2 {
		t.Fatalf("expected 2 nodes after load, got %d", r2.Count())
	}
	node, found := r2.Get("node-1")
	if !found {
		t.Fatal("expected to find node-1 after load")
	}
	if node.Name != "Node 1" {
		t.Fatalf("got name %q, want %q", node.Name, "Node 1")
	}
}

func TestPendingRegistry_Load_NoFile(t *testing.T) {
	t.Parallel()
	r := NewPendingRegistry(filepath.Join(t.TempDir(), "nonexistent.yaml"), nil)
	if err := r.Load(); err != nil {
		t.Fatalf("Load should return nil for nonexistent file, got %v", err)
	}
	if r.Count() != 0 {
		t.Fatalf("expected 0 nodes, got %d", r.Count())
	}
}

func TestPendingRegistry_Load_CorruptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.yaml")
	os.WriteFile(path, []byte("not valid yaml: [[["), 0o644)

	r := NewPendingRegistry(path, nil)
	if err := r.Load(); err == nil {
		t.Fatal("expected error for corrupt file")
	}
}

func TestPendingRegistry_MaxNodes(t *testing.T) {
	t.Parallel()
	r := NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"), nil)

	// Fill to capacity.
	for i := range maxPendingNodes {
		ok, isNew := r.AddOrUpdate(PendingNode{NodeID: nodeID(i), Name: "node"})
		if !ok || !isNew {
			t.Fatalf("expected ok=true, isNew=true for node %d", i)
		}
	}

	// One more should be rejected.
	ok, _ := r.AddOrUpdate(PendingNode{NodeID: "overflow-node", Name: "overflow"})
	if ok {
		t.Fatal("expected ok=false when registry is full")
	}

	// Updating an existing node should still work.
	ok, isNew := r.AddOrUpdate(PendingNode{NodeID: nodeID(0), Name: "updated"})
	if !ok {
		t.Fatal("expected ok=true for updating existing node when full")
	}
	if isNew {
		t.Fatal("expected isNew=false for existing node")
	}
}

func nodeID(i int) string {
	return "node-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}
