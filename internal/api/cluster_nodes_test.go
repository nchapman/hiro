package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/controlplane"
)

// newClusterTestServer creates a Server configured as a leader with
// NodeRegistry, PendingRegistry, and a disconnect callback wired in.
// Returns all components so tests can inspect and manipulate state.
func newClusterTestServer(t *testing.T) (srv *Server, cp *controlplane.ControlPlane, nr *cluster.NodeRegistry, pr *cluster.PendingRegistry, disconnected *[]string) {
	t.Helper()
	s, cplane := newAuthTestServer(t)
	cplane.SetClusterMode("leader")
	cplane.SetClusterNodeName("test-leader")
	cplane.Save()

	nr = cluster.NewNodeRegistry()
	nr.RegisterHome("test-leader")

	pr = cluster.NewPendingRegistry(filepath.Join(t.TempDir(), "pending.yaml"))

	s.SetNodeRegistry(nr)
	s.SetPendingRegistry(pr)

	var disc []string
	s.SetDisconnectNode(func(id string) { disc = append(disc, id) })

	return s, cplane, nr, pr, &disc
}

// --- Pending Node Tests ---

func TestClusterAPI_ListPending_Empty(t *testing.T) {
	srv, _, _, _, _ := newClusterTestServer(t)

	req := authedRequest(t, srv, "GET", "/api/cluster/pending", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var nodes []any
	json.NewDecoder(rec.Body).Decode(&nodes)
	if len(nodes) != 0 {
		t.Errorf("got %d pending nodes, want 0", len(nodes))
	}
}

func TestClusterAPI_ListPending_WithNodes(t *testing.T) {
	srv, _, _, pr, _ := newClusterTestServer(t)

	pr.AddOrUpdate(cluster.PendingNode{
		NodeID: "node-aaa", Name: "Worker A", Addr: "10.0.0.1:8081",
		FirstSeen: time.Now(), LastSeen: time.Now(),
	})
	pr.AddOrUpdate(cluster.PendingNode{
		NodeID: "node-bbb", Name: "Worker B", Addr: "10.0.0.2:8081",
		FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	req := authedRequest(t, srv, "GET", "/api/cluster/pending", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var nodes []map[string]any
	json.NewDecoder(rec.Body).Decode(&nodes)
	if len(nodes) != 2 {
		t.Fatalf("got %d pending nodes, want 2", len(nodes))
	}

	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n["node_id"].(string)] = true
	}
	if !ids["node-aaa"] || !ids["node-bbb"] {
		t.Errorf("missing expected node IDs: %v", ids)
	}
}

func TestClusterAPI_ApproveNode(t *testing.T) {
	srv, cp, _, pr, _ := newClusterTestServer(t)

	pr.AddOrUpdate(cluster.PendingNode{
		NodeID: "node-approve", Name: "Approvable", Addr: "10.0.0.3:8081",
		FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	req := authedRequest(t, srv, "POST", "/api/cluster/pending/node-approve/approve", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Node should be approved in config.
	if !cp.IsNodeApproved("node-approve") {
		t.Error("node should be approved after API call")
	}

	// Node should be removed from pending.
	if _, ok := pr.Get("node-approve"); ok {
		t.Error("node should be removed from pending after approval")
	}

	// Config should be persisted to disk.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp2, err := controlplane.Load(cp.Path(), logger)
	if err != nil {
		t.Fatalf("reloading config: %v", err)
	}
	if !cp2.IsNodeApproved("node-approve") {
		t.Error("approval should be persisted to disk")
	}
}

func TestClusterAPI_ApproveNode_NotFound(t *testing.T) {
	srv, _, _, _, _ := newClusterTestServer(t)

	req := authedRequest(t, srv, "POST", "/api/cluster/pending/nonexistent/approve", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rec.Code)
	}
}

func TestClusterAPI_DismissNode(t *testing.T) {
	srv, cp, _, pr, _ := newClusterTestServer(t)

	pr.AddOrUpdate(cluster.PendingNode{
		NodeID: "node-dismiss", Name: "Dismissable", Addr: "10.0.0.4:8081",
		FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	req := authedRequest(t, srv, "DELETE", "/api/cluster/pending/node-dismiss", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	// Removed from pending.
	if _, ok := pr.Get("node-dismiss"); ok {
		t.Error("node should be removed from pending")
	}

	// NOT added to approved.
	if cp.IsNodeApproved("node-dismiss") {
		t.Error("dismissed node should not be approved")
	}
}

// --- Approved Node Tests ---

func TestClusterAPI_ListApproved(t *testing.T) {
	srv, cp, _, _, _ := newClusterTestServer(t)

	cp.ApproveNode("n1", "Worker 1")
	cp.ApproveNode("n2", "Worker 2")
	cp.Save()

	req := authedRequest(t, srv, "GET", "/api/cluster/approved", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var nodes map[string]any
	json.NewDecoder(rec.Body).Decode(&nodes)
	if len(nodes) != 2 {
		t.Fatalf("got %d approved nodes, want 2", len(nodes))
	}
	if _, ok := nodes["n1"]; !ok {
		t.Error("missing approved node n1")
	}
	if _, ok := nodes["n2"]; !ok {
		t.Error("missing approved node n2")
	}
}

// --- Revoke Tests ---

func TestClusterAPI_RevokeConnectedNode(t *testing.T) {
	srv, cp, nr, _, disconnected := newClusterTestServer(t)

	// Set up: approved + registered online.
	cp.ApproveNode("node-rev", "RevWorker")
	cp.Save()
	nr.Register("node-rev", "RevWorker", 4, "10.0.0.5:8081", "direct")

	// Revoke via API.
	req := authedRequest(t, srv, "DELETE", "/api/cluster/approved/node-rev", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// No longer approved.
	if cp.IsNodeApproved("node-rev") {
		t.Error("node should not be approved after revocation")
	}

	// Is revoked.
	if !cp.IsNodeRevoked("node-rev") {
		t.Error("node should be revoked")
	}

	// Disconnect was called.
	found := false
	for _, id := range *disconnected {
		if id == "node-rev" {
			found = true
		}
	}
	if !found {
		t.Error("disconnectNode should have been called for node-rev")
	}

	// Unregistered from live registry.
	if _, ok := nr.Get("node-rev"); ok {
		t.Error("node should be unregistered from registry")
	}

	// Critical: does NOT appear in settings response.
	assertNodeAbsentFromSettings(t, srv, "node-rev")
}

func TestClusterAPI_RevokeOfflineNode(t *testing.T) {
	srv, cp, nr, _, _ := newClusterTestServer(t)

	cp.ApproveNode("node-off", "OffWorker")
	cp.Save()
	nr.Register("node-off", "OffWorker", 4, "10.0.0.6:8081", "relay")
	nr.SetOffline("node-off")

	req := authedRequest(t, srv, "DELETE", "/api/cluster/approved/node-off", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	if cp.IsNodeApproved("node-off") {
		t.Error("node should not be approved")
	}
	if _, ok := nr.Get("node-off"); ok {
		t.Error("node should be unregistered from registry")
	}

	assertNodeAbsentFromSettings(t, srv, "node-off")
}

// --- Clear Revoked ---

func TestClusterAPI_ClearRevoked(t *testing.T) {
	srv, cp, _, _, _ := newClusterTestServer(t)

	cp.RevokeNode("node-clr")
	cp.Save()

	if !cp.IsNodeRevoked("node-clr") {
		t.Fatal("precondition: node should be revoked")
	}

	req := authedRequest(t, srv, "DELETE", "/api/cluster/revoked/node-clr", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	if cp.IsNodeRevoked("node-clr") {
		t.Error("node should no longer be revoked")
	}
}

// --- Settings Endpoint Tests ---

func TestClusterAPI_Settings_NodeFiltering(t *testing.T) {
	srv, cp, nr, pr, _ := newClusterTestServer(t)

	// n1: approved + online — should appear
	cp.ApproveNode("n1", "Worker 1")
	nr.Register("n1", "Worker 1", 4, "10.0.0.1:8081", "direct")

	// n2: was approved, now revoked but still in registry — should NOT appear
	cp.ApproveNode("n2", "Worker 2")
	nr.Register("n2", "Worker 2", 4, "10.0.0.2:8081", "relay")
	cp.RevokeNode("n2") // removes from approved, adds to revoked
	cp.Save()

	// n3: pending — should NOT appear in nodes
	pr.AddOrUpdate(cluster.PendingNode{
		NodeID: "n3", Name: "Worker 3", Addr: "10.0.0.3:8081",
		FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	settings := getClusterSettings(t, srv)

	// Check nodes list.
	nodes := settings["nodes"].([]any)
	nodeIDs := map[string]bool{}
	for _, raw := range nodes {
		n := raw.(map[string]any)
		nodeIDs[n["id"].(string)] = true
	}

	if !nodeIDs["home"] {
		t.Error("home node should be in nodes list")
	}
	if !nodeIDs["n1"] {
		t.Error("approved online node n1 should be in nodes list")
	}
	if nodeIDs["n2"] {
		t.Error("revoked node n2 should NOT be in nodes list")
	}
	if nodeIDs["n3"] {
		t.Error("pending node n3 should NOT be in nodes list")
	}

	// Check approved_nodes map — should only contain n1.
	approved, _ := settings["approved_nodes"].(map[string]any)
	if _, ok := approved["n1"]; !ok {
		t.Error("n1 should be in approved_nodes")
	}
	if _, ok := approved["n2"]; ok {
		t.Error("revoked n2 should NOT be in approved_nodes")
	}

	// Check pending count.
	if pc, ok := settings["pending_count"].(float64); !ok || int(pc) != 1 {
		t.Errorf("pending_count should be 1, got %v", settings["pending_count"])
	}
}

func TestClusterAPI_Settings_OfflineApproved(t *testing.T) {
	srv, cp, nr, _, _ := newClusterTestServer(t)

	cp.ApproveNode("n-off", "Offline Worker")
	cp.Save()
	nr.Register("n-off", "Offline Worker", 4, "10.0.0.7:8081", "direct")
	nr.SetOffline("n-off")

	settings := getClusterSettings(t, srv)
	nodes := settings["nodes"].([]any)

	found := false
	for _, raw := range nodes {
		n := raw.(map[string]any)
		if n["id"] == "n-off" {
			found = true
			if n["status"] != "offline" {
				t.Errorf("expected status=offline, got %s", n["status"])
			}
		}
	}
	if !found {
		t.Error("offline approved node should appear in nodes list")
	}
}

// --- Cluster Reset ---

func TestClusterAPI_ClusterReset(t *testing.T) {
	srv, cp, _, _, _ := newClusterTestServer(t)

	// Pre-condition: setup is complete.
	if cp.NeedsSetup() {
		t.Fatal("precondition: setup should be complete")
	}

	// Note: handleClusterReset calls requestRestart in a goroutine after 100ms.
	// We need to set it or the handler will just skip the restart.
	srv.SetRestartFunc(func() {}) // no-op for test

	req := authedRequest(t, srv, "POST", "/api/settings/cluster/reset", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	if !cp.NeedsSetup() {
		t.Error("NeedsSetup should return true after reset")
	}
}

// --- Auth Tests ---

func TestClusterAPI_Unauthenticated(t *testing.T) {
	srv, _, _, _, _ := newClusterTestServer(t)

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/cluster/pending"},
		{"POST", "/api/cluster/pending/fake/approve"},
		{"DELETE", "/api/cluster/pending/fake"},
		{"GET", "/api/cluster/approved"},
		{"DELETE", "/api/cluster/approved/fake"},
		{"DELETE", "/api/cluster/revoked/fake"},
		{"POST", "/api/settings/cluster/reset"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status=%d, want 401", ep.method, ep.path, rec.Code)
		}
	}
}

// --- Helpers ---

// getClusterSettings calls GET /api/settings/cluster and returns the parsed response.
func getClusterSettings(t *testing.T, srv *Server) map[string]any {
	t.Helper()
	req := authedRequest(t, srv, "GET", "/api/settings/cluster", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings/cluster: status=%d, body=%s", rec.Code, rec.Body.String())
	}

	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)
	return result
}

// assertNodeAbsentFromSettings verifies a node ID does not appear in the
// settings cluster response (neither in nodes nor approved_nodes).
func assertNodeAbsentFromSettings(t *testing.T, srv *Server, nodeID string) {
	t.Helper()
	settings := getClusterSettings(t, srv)

	// Check nodes list.
	if rawNodes, ok := settings["nodes"]; ok {
		for _, raw := range rawNodes.([]any) {
			n := raw.(map[string]any)
			if n["id"] == nodeID {
				t.Errorf("node %s should not appear in settings nodes list", nodeID)
			}
		}
	}

	// Check approved_nodes.
	if approved, ok := settings["approved_nodes"]; ok {
		if m, ok := approved.(map[string]any); ok {
			if _, exists := m[nodeID]; exists {
				t.Errorf("node %s should not appear in approved_nodes", nodeID)
			}
		}
	}
}
