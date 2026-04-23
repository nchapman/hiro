package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/nchapman/hiro/internal/cluster"
)

func TestHandleGetClusterSettings_NoControlPlane(t *testing.T) {
	t.Parallel()

	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/settings/cluster", nil)
	rec := httptest.NewRecorder()
	srv.handleGetClusterSettings(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleGetClusterSettings_Standalone(t *testing.T) {
	srv, cp := newAuthTestServer(t)
	cp.SetClusterMode("standalone")
	cp.Save()

	req := authedRequest(t, srv, "GET", "/api/settings/cluster", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["mode"] != "standalone" {
		t.Fatalf("mode = %v, want %q", resp["mode"], "standalone")
	}
	// Standalone should not have leader-specific fields.
	if _, ok := resp["swarm_code"]; ok {
		t.Error("standalone mode should not have swarm_code")
	}
	if _, ok := resp["leader_addr"]; ok {
		t.Error("standalone mode should not have leader_addr")
	}
}

func TestHandleGetClusterSettings_LeaderNoNodes(t *testing.T) {
	srv, _, _, _, _ := newClusterTestServer(t)

	settings := getClusterSettings(t, srv)

	if settings["mode"] != "leader" {
		t.Fatalf("mode = %v, want %q", settings["mode"], "leader")
	}
	if settings["node_name"] != "test-leader" {
		t.Fatalf("node_name = %v, want %q", settings["node_name"], "test-leader")
	}
	if _, ok := settings["swarm_code"]; !ok {
		t.Error("leader mode should have swarm_code")
	}
}

func TestHandleGetClusterSettings_LeaderWithNodes(t *testing.T) {
	srv, cp, nr, _, _ := newClusterTestServer(t)

	cp.ApproveNode("w1", "Worker One")
	cp.Save()
	nr.Register("w1", "Worker One", 4, "10.0.0.1:8081", cluster.ViaDirect)

	settings := getClusterSettings(t, srv)

	nodes, ok := settings["nodes"].([]any)
	if !ok {
		t.Fatal("expected nodes to be an array")
	}

	// Should have home + w1.
	if len(nodes) < 2 {
		t.Fatalf("got %d nodes, want at least 2", len(nodes))
	}

	foundHome := false
	foundW1 := false
	for _, raw := range nodes {
		n, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("node entry is not a map[string]any")
		}
		switch n["id"] {
		case "home":
			foundHome = true
		case "w1":
			foundW1 = true
		}
	}
	if !foundHome {
		t.Error("home node missing from nodes list")
	}
	if !foundW1 {
		t.Error("approved worker w1 missing from nodes list")
	}

	// Check approved_nodes.
	approved, ok := settings["approved_nodes"].(map[string]any)
	if !ok {
		t.Fatal("expected approved_nodes in leader response")
	}
	if _, ok := approved["w1"]; !ok {
		t.Error("w1 should be in approved_nodes")
	}
}

func TestHandleGetClusterSettings_WorkerMode(t *testing.T) {
	srv, cp := newAuthTestServer(t)
	cp.SetClusterMode("worker")
	cp.SetClusterLeaderAddr("10.0.0.1:9090")
	cp.Save()

	workerStatusCalled := false
	srv.SetWorkerStatus(func() string {
		workerStatusCalled = true
		return "connected"
	})

	req := authedRequest(t, srv, "GET", "/api/settings/cluster", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["mode"] != "worker" {
		t.Fatalf("mode = %v, want %q", resp["mode"], "worker")
	}
	if resp["leader_addr"] != "10.0.0.1:9090" {
		t.Fatalf("leader_addr = %v, want %q", resp["leader_addr"], "10.0.0.1:9090")
	}
	if resp["connection_status"] != "connected" {
		t.Fatalf("connection_status = %v, want %q", resp["connection_status"], "connected")
	}
	if !workerStatusCalled {
		t.Error("workerStatus function should have been called")
	}
}

// --- handleClusterReset tests ---

func TestHandleClusterReset_NoControlPlane(t *testing.T) {
	t.Parallel()

	srv := newTestServer()

	req := httptest.NewRequest("POST", "/api/settings/cluster/reset", nil)
	rec := httptest.NewRecorder()
	srv.handleClusterReset(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleClusterReset_Success(t *testing.T) {
	srv, cp, nr, pr, _ := newClusterTestServer(t)
	srv.SetRestartFunc(func() {}) // no-op

	// Add some state to verify it gets cleared.
	cp.ApproveNode("w1", "Worker One")
	cp.Save()
	nr.Register("w1", "Worker One", 4, "10.0.0.1:8081", cluster.ViaDirect)
	pr.AddOrUpdate(newPendingNode("p1", "Pending One"))

	req := authedRequest(t, srv, "POST", "/api/settings/cluster/reset", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify state was cleared.
	if !cp.NeedsSetup() {
		t.Error("NeedsSetup should return true after reset")
	}

	// Pending should be cleared.
	if pr.Count() != 0 {
		t.Errorf("pending count = %d, want 0", pr.Count())
	}
}

func TestHandleClusterReset_DisconnectsNodes(t *testing.T) {
	srv, _, nr, _, disconnected := newClusterTestServer(t)
	srv.SetRestartFunc(func() {}) // no-op

	nr.Register("w1", "Worker One", 4, "10.0.0.1:8081", cluster.ViaDirect)
	nr.Register("w2", "Worker Two", 4, "10.0.0.2:8081", cluster.ViaDirect)

	req := authedRequest(t, srv, "POST", "/api/settings/cluster/reset", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Non-home nodes should have been disconnected.
	if len(*disconnected) < 2 {
		t.Fatalf("expected at least 2 disconnect calls, got %d", len(*disconnected))
	}
}

// --- handleUpdateClusterAdvertise tests ---

func TestHandleUpdateClusterAdvertise_Success(t *testing.T) {
	srv, cp, _, _, _ := newClusterTestServer(t)
	restarted := make(chan struct{}, 1)
	srv.SetRestartFunc(func() { restarted <- struct{}{} })

	body := []byte(`{"addresses":["tcp://203.0.113.4:5000","tcp://198.51.100.2:5000"]}`)
	req := authedRequest(t, srv, "PUT", "/api/settings/cluster/advertise", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := cp.ClusterAdvertiseAddresses()
	want := []string{"tcp://203.0.113.4:5000", "tcp://198.51.100.2:5000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("saved addresses = %v, want %v", got, want)
	}

	// Verify it appears in GET response.
	settings := getClusterSettings(t, srv)
	if raw, ok := settings["advertise_addresses"].([]any); !ok || len(raw) != 2 {
		t.Fatalf("advertise_addresses not in GET response: %v", settings["advertise_addresses"])
	}

	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("restart was not requested")
	}
}

func TestHandleUpdateClusterAdvertise_ClearsWithEmptyList(t *testing.T) {
	srv, cp, _, _, _ := newClusterTestServer(t)
	srv.SetRestartFunc(func() {})
	cp.SetClusterAdvertiseAddresses([]string{"tcp://203.0.113.4:5000"})
	cp.Save()

	body := []byte(`{"addresses":[]}`)
	req := authedRequest(t, srv, "PUT", "/api/settings/cluster/advertise", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := cp.ClusterAdvertiseAddresses(); len(got) != 0 {
		t.Fatalf("expected empty addresses, got %v", got)
	}
}

func TestHandleUpdateClusterAdvertise_RejectsInvalid(t *testing.T) {
	srv, cp, _, _, _ := newClusterTestServer(t)
	srv.SetRestartFunc(func() {})

	// Hostname (not IP) is invalid per ValidateAdvertiseAddress.
	body := []byte(`{"addresses":["tcp://example.com:5000"]}`)
	req := authedRequest(t, srv, "PUT", "/api/settings/cluster/advertise", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := cp.ClusterAdvertiseAddresses(); len(got) != 0 {
		t.Fatalf("addresses should not have been saved, got %v", got)
	}
}

func TestHandleUpdateClusterAdvertise_RejectsTooMany(t *testing.T) {
	srv, cp, _, _, _ := newClusterTestServer(t)
	srv.SetRestartFunc(func() {})

	// Build a list one over the cap with all otherwise-valid entries.
	addrs := make([]string, cluster.MaxAdvertiseAddresses+1)
	for i := range addrs {
		addrs[i] = fmt.Sprintf("tcp://203.0.113.%d:5000", i+1)
	}
	raw, _ := json.Marshal(map[string]any{"addresses": addrs})
	req := authedRequest(t, srv, "PUT", "/api/settings/cluster/advertise", raw)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := cp.ClusterAdvertiseAddresses(); len(got) != 0 {
		t.Fatalf("addresses should not have been saved, got %v", got)
	}
}

func TestHandleUpdateClusterAdvertise_RequiresLeaderMode(t *testing.T) {
	srv, cp := newAuthTestServer(t)
	cp.SetClusterMode("worker")
	cp.Save()

	body := []byte(`{"addresses":["tcp://203.0.113.4:5000"]}`)
	req := authedRequest(t, srv, "PUT", "/api/settings/cluster/advertise", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// --- handleTerminalSessions tests ---

func TestHandleTerminalSessions_NoManager(t *testing.T) {
	t.Parallel()

	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/terminal/sessions", nil)
	rec := httptest.NewRecorder()
	srv.handleTerminalSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body []any
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body) != 0 {
		t.Fatalf("expected empty array, got %d items", len(body))
	}
}

func TestHandleTerminalSessions_WithSessions(t *testing.T) {
	t.Parallel()

	srv := newTestServer()
	mgr := NewTerminalSessionManager(t.TempDir(), srv.logger)
	t.Cleanup(func() { mgr.Shutdown() })
	srv.termSessions = mgr

	_, err := mgr.Create("home", 80, 24)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/terminal/sessions", nil)
	rec := httptest.NewRecorder()
	srv.handleTerminalSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body []TerminalSessionInfo
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body) != 1 {
		t.Fatalf("expected 1 session, got %d", len(body))
	}
}

// --- handleTerminalNodes tests ---

func TestHandleTerminalNodes_NoRegistry(t *testing.T) {
	t.Parallel()

	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/terminal/nodes", nil)
	rec := httptest.NewRecorder()
	srv.handleTerminalNodes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body []map[string]any
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body) != 1 {
		t.Fatalf("expected 1 node (home), got %d", len(body))
	}
	if body[0]["id"] != "home" {
		t.Fatalf("node id = %v, want %q", body[0]["id"], "home")
	}
	if body[0]["is_home"] != true {
		t.Fatalf("is_home = %v, want true", body[0]["is_home"])
	}
}

func TestHandleTerminalNodes_WithRegistry(t *testing.T) {
	srv, _, nr, _, _ := newClusterTestServer(t)

	nr.Register("w1", "Worker One", 4, "10.0.0.1:8081", cluster.ViaDirect)

	req := authedRequest(t, srv, "GET", "/api/terminal/nodes", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body []map[string]any
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body) < 2 {
		t.Fatalf("expected at least 2 nodes (home + w1), got %d", len(body))
	}

	foundHome := false
	foundW1 := false
	for _, n := range body {
		switch n["id"] {
		case "home":
			foundHome = true
		case "w1":
			foundW1 = true
		}
	}
	if !foundHome {
		t.Error("home node missing")
	}
	if !foundW1 {
		t.Error("worker node w1 missing")
	}
}

// --- helpers ---

func newPendingNode(id, name string) cluster.PendingNode {
	now := time.Now()
	return cluster.PendingNode{
		NodeID:    id,
		Name:      name,
		Addr:      "10.0.0.1:8081",
		FirstSeen: now,
		LastSeen:  now,
	}
}
