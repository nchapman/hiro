//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestE2E_InstanceStopStart spawns a persistent instance, stops it via REST,
// verifies it's stopped, restarts it, and verifies it's running again.
func TestE2E_InstanceStopStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	id := spawnPersistentAgent(t, ctx, "lifecycle-stop-start")
	t.Cleanup(func() { deleteInstance(t, id) })

	// Stop the instance.
	code, body := postInstance(t, id, "stop")
	if code != http.StatusNoContent {
		t.Fatalf("stop: expected 204, got %d: %s", code, body)
	}

	// Verify it's stopped in the instance list.
	inst, ok := findInstance(t, "lifecycle-stop-start")
	if !ok {
		t.Fatal("instance disappeared after stop")
	}
	if inst.Status != "stopped" {
		t.Errorf("expected status=stopped, got %q", inst.Status)
	}

	// Start it again.
	code, body = postInstance(t, id, "start")
	if code != http.StatusNoContent {
		t.Fatalf("start: expected 204, got %d: %s", code, body)
	}

	// Verify it's running.
	inst, ok = findInstance(t, "lifecycle-stop-start")
	if !ok {
		t.Fatal("instance disappeared after start")
	}
	if inst.Status != "running" {
		t.Errorf("expected status=running, got %q", inst.Status)
	}
}

// TestE2E_InstanceDelete spawns a persistent instance, deletes it via REST,
// and verifies it's gone from the instance list.
func TestE2E_InstanceDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	id := spawnPersistentAgent(t, ctx, "lifecycle-delete")
	t.Logf("spawned instance %s", id)

	// Delete it.
	code, body := deleteInstance(t, id)
	t.Logf("delete returned %d: %s", code, body)
	if code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", code, body)
	}

	// Verify it's gone — search by ID since agent name may match other instances.
	for _, inst := range listInstances(t) {
		if inst.ID == id {
			t.Errorf("instance %s still present after delete (status=%s)", id, inst.Status)
		}
	}
}

// TestE2E_RootProtection verifies that the coordinator (root instance) cannot
// be stopped or deleted via REST.
func TestE2E_RootProtection(t *testing.T) {
	// Cannot stop coordinator.
	code, _ := postInstance(t, coordinatorID, "stop")
	if code != http.StatusForbidden {
		t.Errorf("stop coordinator: expected 403, got %d", code)
	}

	// Cannot delete coordinator.
	code, _ = deleteInstance(t, coordinatorID)
	if code != http.StatusForbidden {
		t.Errorf("delete coordinator: expected 403, got %d", code)
	}

	// Coordinator should still be running.
	inst, ok := findInstance(t, "coordinator")
	if !ok {
		t.Fatal("coordinator disappeared")
	}
	if inst.Status != "running" {
		t.Errorf("coordinator status: expected running, got %q", inst.Status)
	}
}

// TestE2E_SessionClear verifies that POST /api/instances/{id}/clear creates a
// new session: memory persists (instance-level) but todos reset (session-level).
func TestE2E_SessionClear(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	id := spawnPersistentAgent(t, ctx, "lifecycle-clear")
	t.Cleanup(func() { deleteInstance(t, id) })

	// Talk to the child instance: write memory and create a todo.
	cs := openChat(t, ctx, "")
	defer cs.close()

	cs.chat(ctx, `Send a message to the instance named "lifecycle-clear" telling it: "Write 'favorite_color: purple' to your memory.md file. Then use the TodoWrite tool to create one task: 'test session clear'." Report what it said.`)

	// Verify memory and todos exist before clear.
	instDir := instanceDir(t, id)
	memContent := containerExec(t, "cat", instDir+"/memory.md")
	if !strings.Contains(strings.ToLower(memContent), "purple") {
		t.Fatalf("expected 'purple' in memory before clear, got %q", memContent)
	}
	sessDir := activeSessionDir(t, id)
	todosContent := containerExec(t, "cat", sessDir+"/todos.yaml")
	if !strings.Contains(strings.ToLower(todosContent), "session clear") {
		t.Fatalf("expected 'session clear' in todos before clear, got %q", todosContent)
	}

	oldSessDir := sessDir

	// Clear the instance (creates new session).
	code, body := postInstance(t, id, "clear")
	if code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", code, body)
	}

	// Verify new session directory was created.
	newSessDir := activeSessionDir(t, id)
	if newSessDir == oldSessDir {
		t.Error("session dir did not change after clear")
	}

	// Memory should survive (instance-level).
	memContent = containerExec(t, "cat", instDir+"/memory.md")
	if !strings.Contains(strings.ToLower(memContent), "purple") {
		t.Errorf("memory lost after clear: expected 'purple', got %q", memContent)
	}

	// Todos should be gone (session-level — new session has no todos.yaml).
	if containerFileExists(t, newSessDir+"/todos.yaml") {
		content := containerExec(t, "cat", newSessDir+"/todos.yaml")
		t.Errorf("todos.yaml should not exist in new session, got: %s", content)
	}
}

// TestE2E_WebSocketNonExistentInstance verifies that connecting a WebSocket to
// a non-existent instance returns an error (not a successful upgrade).
func TestE2E_WebSocketNonExistentInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := strings.Replace(baseURL, "http://", "ws://", 1) + "/ws/chat?instance_id=does-not-exist"
	host := strings.TrimPrefix(baseURL, "http://")
	host = strings.TrimPrefix(host, "https://")

	headers := http.Header{"Origin": {"http://" + host}}
	if httpClient != nil && httpClient.Jar != nil {
		if u, err := parseBaseURL(); err == nil {
			for _, c := range httpClient.Jar.Cookies(u) {
				headers.Add("Cookie", c.String())
			}
		}
	}

	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected dial to fail for non-existent instance, but got a connection")
	}
	if err == nil {
		t.Fatal("expected error for non-existent instance")
	}
	// The server should reject before upgrading — check for 404.
	if resp != nil && resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	t.Logf("non-existent instance: err=%v", err)
}

// TestE2E_WebSocketStoppedInstance verifies that connecting a WebSocket to
// a stopped instance returns a conflict error.
func TestE2E_WebSocketStoppedInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	id := spawnPersistentAgent(t, ctx, "lifecycle-ws-stopped")
	t.Cleanup(func() { deleteInstance(t, id) })

	// Stop the instance.
	code, body := postInstance(t, id, "stop")
	if code != http.StatusNoContent {
		t.Fatalf("stop: expected 204, got %d: %s", code, body)
	}

	// Try to connect WebSocket to the stopped instance.
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1) + "/ws/chat?instance_id=" + id
	host := strings.TrimPrefix(baseURL, "http://")
	host = strings.TrimPrefix(host, "https://")

	headers := http.Header{"Origin": {"http://" + host}}
	if httpClient != nil && httpClient.Jar != nil {
		if u, err := parseBaseURL(); err == nil {
			for _, c := range httpClient.Jar.Cookies(u) {
				headers.Add("Cookie", c.String())
			}
		}
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()

	conn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected dial to fail for stopped instance, but got a connection")
	}
	if err == nil {
		t.Fatal("expected error for stopped instance")
	}
	// The server should reject with 409 Conflict.
	if resp != nil && resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
	t.Logf("stopped instance: err=%v", err)
}

// parseBaseURL is a small helper for the WebSocket tests.
func parseBaseURL() (*url.URL, error) {
	return url.Parse(baseURL)
}
