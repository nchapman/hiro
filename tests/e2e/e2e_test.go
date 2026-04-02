//go:build e2e

// Package e2e contains end-to-end tests that run against a live hiro server
// in Docker. Tests communicate exclusively over HTTP/WebSocket — no internal
// packages are imported.
//
// Required environment:
//
//	HIRO_E2E_URL        — base URL of the running server (e.g. http://localhost:8080)
//	HIRO_E2E_CONTAINER  — Docker container name for filesystem access (e.g. hiro-e2e)
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var (
	baseURL       string
	containerName string
	coordinatorID string
	httpClient    *http.Client // shared client with cookie jar for auth
)

func TestMain(m *testing.M) {
	baseURL = os.Getenv("HIRO_E2E_URL")
	if baseURL == "" {
		fmt.Println("HIRO_E2E_URL not set — skipping e2e tests")
		os.Exit(0)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	containerName = os.Getenv("HIRO_E2E_CONTAINER")
	if containerName == "" {
		containerName = "hiro-e2e"
	}

	// Create HTTP client with cookie jar for authenticated requests.
	jar, _ := cookiejar.New(nil)
	httpClient = &http.Client{Jar: jar, Timeout: 30 * time.Second}

	// Wait for server to be healthy (container startup + binary init).
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer healthCancel()
	if err := waitHealthy(healthCtx); err != nil {
		fmt.Fprintf(os.Stderr, "server never became healthy: %v\n", err)
		os.Exit(1)
	}

	// Run setup to configure the LLM provider (fresh container has no config).
	if err := runSetup(); err != nil {
		fmt.Fprintf(os.Stderr, "setup failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("setup complete")

	// Wait for the coordinator agent to be ready (requires LLM call to spawn).
	coordCtx, coordCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer coordCancel()
	id, err := waitForCoordinator(coordCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coordinator never appeared: %v\n", err)
		os.Exit(1)
	}
	coordinatorID = id
	fmt.Printf("coordinator ready: %s\n", coordinatorID)

	os.Exit(m.Run())
}

// runSetup calls POST /api/setup to configure the LLM provider and admin password.
// Reads HIRO_API_KEY, HIRO_PROVIDER, and HIRO_MODEL from environment.
// The session cookie is stored in the shared httpClient's cookie jar.
func runSetup() error {
	apiKey := os.Getenv("HIRO_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("HIRO_API_KEY must be set")
	}
	provider := os.Getenv("HIRO_PROVIDER")
	if provider == "" {
		provider = "anthropic"
	}
	model := os.Getenv("HIRO_MODEL")

	body, _ := json.Marshal(map[string]string{
		"password":      "e2e-test-password-12345",
		"mode":          "standalone",
		"node_name":     "e2e-test",
		"provider_type": provider,
		"api_key":       apiKey,
		"default_model": model,
	})

	host := strings.TrimPrefix(baseURL, "http://")
	host = strings.TrimPrefix(host, "https://")

	req, _ := http.NewRequest("POST", baseURL+"/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+host)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST /api/setup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /api/setup: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// --- HTTP helpers ---

func waitHealthy(ctx context.Context) error {
	// Health check runs before setup, so use a plain client (no auth needed).
	var lastErr error
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for healthy server; last error: %v", lastErr)
		}
		resp, err := http.Get(baseURL + "/api/health")
		if err != nil {
			lastErr = err
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

type instanceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	ParentID    string `json:"parent_id,omitempty"`
	Model       string `json:"model,omitempty"`
}

func listInstances(t *testing.T) []instanceInfo {
	t.Helper()
	resp, err := httpClient.Get(baseURL + "/api/instances")
	if err != nil {
		t.Fatalf("GET /api/instances: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/instances: status %d: %s", resp.StatusCode, body)
	}
	var instances []instanceInfo
	if err := json.NewDecoder(resp.Body).Decode(&instances); err != nil {
		t.Fatalf("decoding instances: %v", err)
	}
	return instances
}

func waitForCoordinator(ctx context.Context) (string, error) {
	fmt.Printf("waiting for coordinator at %s\n", baseURL)
	for {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timed out waiting for coordinator: %w", ctx.Err())
		}
		reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
		req, _ := http.NewRequestWithContext(reqCtx, "GET", baseURL+"/api/instances", nil)
		resp, err := httpClient.Do(req)
		reqCancel()
		if err != nil {
			fmt.Printf("  poll error: %v\n", err)
			time.Sleep(time.Second)
			continue
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Printf("  poll status=%d body=%s\n", resp.StatusCode, body)
			time.Sleep(time.Second)
			continue
		}
		var instances []instanceInfo
		if err := json.NewDecoder(resp.Body).Decode(&instances); err == nil {
			for _, inst := range instances {
				if inst.Mode == "coordinator" {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					return inst.ID, nil
				}
			}
		}
		resp.Body.Close()
		time.Sleep(time.Second)
	}
}

// --- WebSocket chat client ---

// chatMessage mirrors the server's ChatMessage type.
type chatMessage struct {
	Type    string `json:"type"`              // "message", "delta", "done", "error", "system"
	Role    string `json:"role,omitempty"`     // "user" or "assistant"
	Content string `json:"content,omitempty"`
}

// chatSession wraps a WebSocket connection to /ws/chat.
type chatSession struct {
	conn *websocket.Conn
	t    *testing.T
}

// openChat opens a WebSocket chat session to the given agent.
// If agentID is empty, it connects to the default (coordinator).
func openChat(t *testing.T, ctx context.Context, agentID string) *chatSession {
	t.Helper()

	wsURL := strings.Replace(baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL += "/ws/chat"
	if agentID != "" {
		wsURL += "?instance_id=" + agentID
	}

	// Origin must match the server's Host for the origin check.
	host := strings.TrimPrefix(baseURL, "http://")
	host = strings.TrimPrefix(host, "https://")

	// Build headers with origin and session cookie for auth.
	headers := http.Header{
		"Origin": {"http://" + host},
	}
	if httpClient != nil && httpClient.Jar != nil {
		if u, parseErr := url.Parse(baseURL); parseErr == nil {
			for _, c := range httpClient.Jar.Cookies(u) {
				headers.Add("Cookie", c.String())
			}
		}
	}

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	// Allow large messages (LLM responses can be verbose).
	conn.SetReadLimit(1 << 20) // 1MB

	return &chatSession{conn: conn, t: t}
}

// send sends a user message.
func (c *chatSession) send(ctx context.Context, text string) {
	c.t.Helper()
	msg := chatMessage{Type: "message", Content: text}
	if err := wsjson.Write(ctx, c.conn, msg); err != nil {
		c.t.Fatalf("sending message: %v", err)
	}
}

// readResponse reads delta messages until a "done" or "error" message.
// Returns the full accumulated text and the individual deltas.
func (c *chatSession) readResponse(ctx context.Context) (fullText string, deltas []string) {
	c.t.Helper()
	var sb strings.Builder
	for {
		var msg chatMessage
		if err := wsjson.Read(ctx, c.conn, &msg); err != nil {
			c.t.Fatalf("reading response: %v", err)
		}
		switch msg.Type {
		case "delta":
			sb.WriteString(msg.Content)
			deltas = append(deltas, msg.Content)
		case "done":
			return sb.String(), deltas
		case "error":
			c.t.Fatalf("server error: %s", msg.Content)
		case "system":
			// slash command response — treat as text
			return msg.Content, nil
		}
	}
}

// close closes the WebSocket connection.
func (c *chatSession) close() {
	c.conn.Close(websocket.StatusNormalClosure, "")
}

// chat is a convenience: send a message and read the full response.
func (c *chatSession) chat(ctx context.Context, text string) string {
	c.t.Helper()
	c.send(ctx, text)
	resp, _ := c.readResponse(ctx)
	return resp
}

// --- Docker exec helpers ---

// containerExec runs a command inside the hiro container and returns stdout.
func containerExec(t *testing.T, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"exec", containerName}, args...)
	out, err := exec.Command("docker", cmdArgs...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("docker exec %v: %v\nstderr: %s", args, err, exitErr.Stderr)
		}
		t.Fatalf("docker exec %v: %v", args, err)
	}
	return string(out)
}

// containerFileExists checks if a file exists inside the container.
func containerFileExists(t *testing.T, path string) bool {
	t.Helper()
	err := exec.Command("docker", "exec", containerName, "test", "-f", path).Run()
	return err == nil
}

// containerWriteFile writes content to a file inside the container.
// Uses positional parameters to avoid shell injection.
func containerWriteFile(t *testing.T, filePath, content string) {
	t.Helper()
	dir := path.Dir(filePath)
	if out, err := exec.Command("docker", "exec", containerName, "mkdir", "-p", dir).CombinedOutput(); err != nil {
		t.Fatalf("mkdir -p %s in container: %v\n%s", dir, err, out)
	}
	// Use sh -c with $1 positional param to keep path as data, not code.
	cmd := exec.Command("docker", "exec", "-i", containerName, "sh", "-c", `cat > "$1"`, "--", filePath)
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("writing %s in container: %v\n%s", filePath, err, out)
	}
}

// --- REST helpers ---

// postInstance sends a POST to /api/instances/{id}/{action} and returns the status code and body.
func postInstance(t *testing.T, instanceID, action string) (int, string) {
	t.Helper()
	resp, err := httpClient.Post(baseURL+"/api/instances/"+instanceID+"/"+action, "", nil)
	if err != nil {
		t.Fatalf("POST /api/instances/%s/%s: %v", instanceID, action, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// deleteInstance sends a DELETE to /api/instances/{id} and returns the status code and body.
func deleteInstance(t *testing.T, instanceID string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("DELETE", baseURL+"/api/instances/"+instanceID, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/instances/%s: %v", instanceID, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// findInstance searches the instance list for an instance with the given ID.
// Returns the instance info and true if found, zero value and false otherwise.
func findInstance(t *testing.T, id string) (instanceInfo, bool) {
	t.Helper()
	for _, inst := range listInstances(t) {
		if inst.ID == id {
			return inst, true
		}
	}
	return instanceInfo{}, false
}

// spawnPersistentAgent writes an agent definition and asks the coordinator to
// spawn it as a persistent instance. Returns the instance ID.
func spawnPersistentAgent(t *testing.T, ctx context.Context, name string) string {
	t.Helper()

	containerWriteFile(t, fmt.Sprintf("/hiro/agents/%s/agent.md", name), fmt.Sprintf(`---
name: %s
allowed_tools: [Read, Write, Edit, Glob, Grep, Bash]
---

You are a test agent. Be concise.`, name))

	// Snapshot existing instance IDs so we can find the new one.
	before := make(map[string]bool)
	for _, inst := range listInstances(t) {
		before[inst.ID] = true
	}

	cs := openChat(t, ctx, "")
	defer cs.close()

	cs.chat(ctx, fmt.Sprintf(`Use CreatePersistentInstance with agent "%s". Then use SendMessage to send it "Acknowledge you are ready." Do not use any other tools.`, name))

	// Find the new instance (not in the snapshot, child of coordinator).
	for _, inst := range listInstances(t) {
		if !before[inst.ID] && inst.Mode == "persistent" && inst.ParentID == coordinatorID {
			return inst.ID
		}
	}
	t.Fatalf("persistent instance %q not found after spawn", name)
	return ""
}

// instanceDir returns the container path to an instance's directory.
func instanceDir(_ *testing.T, instanceID string) string {
	return "/hiro/instances/" + instanceID
}

// activeSessionDir returns the container path to an instance's active session directory.
// Session IDs are UUID v7 (time-ordered), so the last entry alphabetically is the newest.
func activeSessionDir(t *testing.T, instanceID string) string {
	t.Helper()
	out := containerExec(t, "sh", "-c", fmt.Sprintf("ls -1 /hiro/instances/%s/sessions/ | tail -1", instanceID))
	sessionID := strings.TrimSpace(out)
	if sessionID == "" {
		t.Fatalf("no sessions found for instance %s", instanceID)
	}
	return fmt.Sprintf("/hiro/instances/%s/sessions/%s", instanceID, sessionID)
}
