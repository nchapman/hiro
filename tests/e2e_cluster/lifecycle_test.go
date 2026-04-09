//go:build e2e_cluster

package e2e_cluster

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCluster_Z_Lifecycle runs after all other cluster tests (alphabetical
// ordering by file name + Z_ prefix). These tests are destructive — they
// revoke and restart the worker, so they must run last.
func TestCluster_Z_Lifecycle(t *testing.T) {
	var nodeID string
	t.Run("RevokeConnectedWorker", func(t *testing.T) {
		nodeID = testRevokeConnectedWorker(t)
	})
	t.Run("ReconnectAfterClearRevocation", func(t *testing.T) {
		if nodeID == "" {
			t.Skip("no node ID from previous subtest")
		}
		testReconnectAfterClearRevocation(t, nodeID)
	})
}

func testRevokeConnectedWorker(t *testing.T) string {
	// Step 1: Confirm the worker is online.
	nodeID := getWorkerNodeID(t)
	if nodeID == "" {
		t.Fatal("no worker node found in cluster settings")
	}
	t.Logf("worker node ID: %s", nodeID)

	// Step 2: Revoke the worker.
	req, _ := http.NewRequest("DELETE",
		fmt.Sprintf("%s/api/cluster/approved/%s", baseURL, nodeID), nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("revoke request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d, want 200", resp.StatusCode)
	}

	// Step 3: Worker should disappear from leader's settings.
	waitForNodeAbsent(t, nodeID, 15*time.Second)

	// Step 4: Worker should detect the revocation.
	waitForWorkerStatus(t, "revoked", 15*time.Second)

	// Step 5: Worker should NOT reappear in pending list.
	t.Log("waiting 10s to verify worker does not reappear as pending...")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pending := getPendingNodes(t)
		for _, n := range pending {
			if n == nodeID {
				t.Fatalf("revoked worker reappeared in pending list")
			}
		}
		time.Sleep(2 * time.Second)
	}
	return nodeID
}

func testReconnectAfterClearRevocation(t *testing.T, nodeID string) {
	// Step 1: Clear the revocation.
	req, _ := http.NewRequest("DELETE",
		fmt.Sprintf("%s/api/cluster/revoked/%s", baseURL, nodeID), nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("clear revocation: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear revocation status=%d, want 200", resp.StatusCode)
	}

	// Step 2: Restart the worker container so it reconnects with its existing identity.
	t.Log("restarting worker container...")
	if out, err := exec.Command("docker", "restart", workerContainer).CombinedOutput(); err != nil {
		t.Fatalf("docker restart: %v\n%s", err, out)
	}

	// Step 3: Worker should appear in pending list (same identity, no longer revoked).
	t.Log("waiting for worker to appear in pending list...")
	var pendingNodeID string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		pending := getPendingNodes(t)
		for _, id := range pending {
			// The worker keeps its identity.key across restarts, so same ID.
			if id == nodeID {
				pendingNodeID = id
				break
			}
		}
		if pendingNodeID != "" {
			break
		}
		time.Sleep(time.Second)
	}
	if pendingNodeID == "" {
		t.Fatal("worker did not reappear in pending list after restart")
	}
	t.Logf("worker pending with ID: %s", pendingNodeID)

	// Step 4: Re-approve the worker.
	approveReq, _ := http.NewRequest("POST",
		fmt.Sprintf("%s/api/cluster/pending/%s/approve", baseURL, pendingNodeID), nil)
	approveResp, err := httpClient.Do(approveReq)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusOK {
		t.Fatalf("approve status=%d, want 200", approveResp.StatusCode)
	}

	// Step 5: Worker should appear online in leader's settings.
	t.Log("waiting for worker to come online...")
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		nodes := getClusterNodeList(t)
		for _, n := range nodes {
			if n["id"] == pendingNodeID && n["status"] == "online" {
				t.Logf("worker back online: %s", pendingNodeID)
				goto online
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("worker did not come back online after re-approval")
online:

	// Step 6: Verify connectivity — write a file on leader, check it on worker.
	marker := fmt.Sprintf("lifecycle-reconnect-%d", time.Now().UnixNano())
	apiWriteFile(t, "workspace/lifecycle-test.txt", marker)

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("docker", "exec", workerContainer,
			"cat", "/hiro/workspace/lifecycle-test.txt").Output()
		if err == nil && strings.Contains(string(out), marker) {
			t.Log("file sync verified after reconnection")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Error("file did not sync to worker after reconnection")
}

// --- Helpers ---

// getWorkerNodeID returns the first non-home node ID from cluster settings.
func getWorkerNodeID(t *testing.T) string {
	t.Helper()
	nodes := getClusterNodeList(t)
	for _, n := range nodes {
		if n["is_home"] != true {
			return n["id"].(string)
		}
	}
	return ""
}

// getClusterNodeList returns the "nodes" array from GET /api/settings/cluster.
func getClusterNodeList(t *testing.T) []map[string]any {
	t.Helper()
	resp, err := httpClient.Get(baseURL + "/api/settings/cluster")
	if err != nil {
		t.Fatalf("GET /api/settings/cluster: %v", err)
	}
	defer resp.Body.Close()

	var settings map[string]any
	json.NewDecoder(resp.Body).Decode(&settings)

	rawNodes, ok := settings["nodes"]
	if !ok {
		return nil
	}

	var result []map[string]any
	for _, raw := range rawNodes.([]any) {
		result = append(result, raw.(map[string]any))
	}
	return result
}

// getPendingNodes returns the node IDs of all pending nodes.
func getPendingNodes(t *testing.T) []string {
	t.Helper()
	resp, err := httpClient.Get(baseURL + "/api/cluster/pending")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var nodes []map[string]any
	json.NewDecoder(resp.Body).Decode(&nodes)

	var ids []string
	for _, n := range nodes {
		if id, ok := n["node_id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// waitForNodeAbsent polls until the node ID is absent from the leader's
// cluster settings (both nodes list and approved_nodes).
func waitForNodeAbsent(t *testing.T, nodeID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(baseURL + "/api/settings/cluster")
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var settings map[string]any
		json.NewDecoder(resp.Body).Decode(&settings)
		resp.Body.Close()

		// Check nodes list.
		found := false
		if rawNodes, ok := settings["nodes"]; ok {
			for _, raw := range rawNodes.([]any) {
				n := raw.(map[string]any)
				if n["id"] == nodeID {
					found = true
				}
			}
		}

		// Check approved_nodes.
		if approved, ok := settings["approved_nodes"].(map[string]any); ok {
			if _, exists := approved[nodeID]; exists {
				found = true
			}
		}

		if !found {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("node %s still present in settings after %v", nodeID, timeout)
}

// waitForWorkerStatus checks the worker's own /api/settings/cluster endpoint
// via docker exec and waits for connection_status to match expected.
func waitForWorkerStatus(t *testing.T, expected string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus string
	for time.Now().Before(deadline) {
		out, err := exec.Command("docker", "exec", workerContainer,
			"curl", "-sf", "http://localhost:8120/api/settings/cluster").Output()
		if err == nil {
			var settings map[string]any
			if json.Unmarshal(out, &settings) == nil {
				if status, ok := settings["connection_status"].(string); ok {
					lastStatus = status
					if status == expected {
						t.Logf("worker connection_status=%s", status)
						return
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("worker connection_status=%q after %v, want %q", lastStatus, timeout, expected)
}
