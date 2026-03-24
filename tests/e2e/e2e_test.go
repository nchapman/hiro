//go:build e2e

// Package e2e contains end-to-end tests that run against a live hive server
// in Docker. Tests communicate exclusively over HTTP/WebSocket — no internal
// packages are imported.
//
// Required environment:
//
//	HIVE_E2E_URL        — base URL of the running server (e.g. http://localhost:8080)
//	HIVE_E2E_CONTAINER  — Docker container name for filesystem access (e.g. hive-e2e)
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
)

func TestMain(m *testing.M) {
	baseURL = os.Getenv("HIVE_E2E_URL")
	if baseURL == "" {
		fmt.Println("HIVE_E2E_URL not set — skipping e2e tests")
		os.Exit(0)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	containerName = os.Getenv("HIVE_E2E_CONTAINER")
	if containerName == "" {
		containerName = "hive-e2e"
	}

	// Wait for server to be healthy (container startup + binary init).
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer healthCancel()
	if err := waitHealthy(healthCtx); err != nil {
		fmt.Fprintf(os.Stderr, "server never became healthy: %v\n", err)
		os.Exit(1)
	}

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

// --- HTTP helpers ---

func waitHealthy(ctx context.Context) error {
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

type sessionInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	ParentID    string `json:"parent_id,omitempty"`
}

func listSessions(t *testing.T) []sessionInfo {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/sessions: status %d: %s", resp.StatusCode, body)
	}
	var sessions []sessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decoding sessions: %v", err)
	}
	return sessions
}

func waitForCoordinator(ctx context.Context) (string, error) {
	for {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timed out waiting for coordinator: %w", ctx.Err())
		}
		resp, err := http.Get(baseURL + "/api/sessions")
		if err == nil && resp.StatusCode == 200 {
			var sessions []sessionInfo
			if err := json.NewDecoder(resp.Body).Decode(&sessions); err == nil {
				for _, a := range sessions {
					if a.Name == "coordinator" {
						resp.Body.Close()
						return a.ID, nil
					}
				}
			}
			resp.Body.Close()
		} else if resp != nil {
			resp.Body.Close()
		}
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
		wsURL += "?session_id=" + agentID
	}

	// Origin must match the server's Host for the origin check.
	host := strings.TrimPrefix(baseURL, "http://")
	host = strings.TrimPrefix(host, "https://")

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Origin": {"http://" + host},
		},
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

// containerExec runs a command inside the hive container and returns stdout.
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

// sessionDir returns the container path to an agent's session directory.
func sessionDir(t *testing.T, agentID string) string {
	t.Helper()
	return "/hive/sessions/" + agentID
}
