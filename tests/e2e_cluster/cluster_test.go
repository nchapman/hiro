//go:build e2e_cluster

// Package e2e_cluster contains end-to-end tests for Hiro's leader-worker
// clustering. Tests verify the full flow: worker connects to leader,
// files sync bidirectionally, operator spawns agents on the worker
// node, and those agents execute tools remotely with results flowing
// back through the leader.
//
// Required environment:
//
//	HIRO_E2E_URL            — leader's HTTP base URL (e.g. http://localhost:8120)
//	HIRO_LEADER_CONTAINER   — leader Docker container ID
//	HIRO_WORKER_CONTAINER   — worker Docker container ID
//	HIRO_API_KEY            — LLM provider API key
package e2e_cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var (
	baseURL         string
	leaderContainer string
	workerContainer string
	httpClient      *http.Client
	operatorID      string
)

func TestMain(m *testing.M) {
	baseURL = os.Getenv("HIRO_E2E_URL")
	if baseURL == "" {
		fmt.Println("HIRO_E2E_URL not set — skipping cluster e2e tests")
		os.Exit(0)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	leaderContainer = os.Getenv("HIRO_LEADER_CONTAINER")
	workerContainer = os.Getenv("HIRO_WORKER_CONTAINER")
	if leaderContainer == "" || workerContainer == "" {
		fmt.Println("HIRO_LEADER_CONTAINER and HIRO_WORKER_CONTAINER must be set")
		os.Exit(1)
	}

	jar, _ := cookiejar.New(nil)
	httpClient = &http.Client{Jar: jar}

	// Wait for leader to be healthy.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := waitHealthy(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "leader never became healthy: %v\n", err)
		os.Exit(1)
	}

	// Set up LLM provider (leader mode is pre-configured via mounted config.yaml).
	if err := runSetup(); err != nil {
		fmt.Fprintf(os.Stderr, "setup failed: %v\n", err)
		os.Exit(1)
	}

	// Approve the worker node — it connects and enters pending state.
	approveCtx, approveCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer approveCancel()
	if err := approveFirstPendingNode(approveCtx); err != nil {
		fmt.Fprintf(os.Stderr, "worker approval failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("worker node approved")

	// Wait for operator.
	coordCtx, coordCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer coordCancel()
	id, err := waitForOperator(coordCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "operator never appeared: %v\n", err)
		os.Exit(1)
	}
	operatorID = id
	fmt.Printf("operator ready: %s\n", operatorID)

	os.Exit(m.Run())
}

// --- Tests ---

// TestCluster_WorkerNodeRegistered verifies the worker node connected
// to the leader and is still running (it would exit on registration failure).
func TestCluster_WorkerNodeRegistered(t *testing.T) {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", workerContainer).Output()
	if err != nil {
		t.Fatalf("inspecting worker container: %v", err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		logs, _ := exec.Command("docker", "logs", "--tail=50", workerContainer).CombinedOutput()
		t.Fatalf("worker container not running. Logs:\n%s", logs)
	}
}

// TestCluster_OperatorSeesWorkerNode asks the operator to list nodes
// and verifies the worker appears.
func TestCluster_OperatorSeesWorkerNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cs := openChat(t, ctx)
	defer cs.close()

	resp := cs.chat(ctx, `Use the ListNodes tool and tell me what nodes are available. Just list them.`)
	t.Logf("operator response: %s", resp)

	// The response should mention at least the home node and the worker.
	if !strings.Contains(strings.ToLower(resp), "home") {
		t.Error("expected operator to mention 'home' node")
	}
}

// TestCluster_FileSyncLeaderToWorker writes a file on the leader via the
// REST API (which triggers fsnotify) and verifies it appears on the worker.
func TestCluster_FileSyncLeaderToWorker(t *testing.T) {
	testContent := fmt.Sprintf("leader-to-worker-%d", time.Now().UnixNano())

	// Write via the Hiro API so the leader's hiro process creates the file
	// (triggering fsnotify for the file sync watcher).
	apiWriteFile(t, "workspace/sync-l2w-test.txt", testContent)

	var workerContent string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("docker", "exec", workerContainer, "cat", "/home/hiro/workspace/sync-l2w-test.txt").Output()
		if err == nil && strings.TrimSpace(string(out)) == testContent {
			workerContent = strings.TrimSpace(string(out))
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if workerContent != testContent {
		t.Errorf("leader→worker sync failed: worker has %q, want %q", workerContent, testContent)
	}
}

// TestCluster_FileSyncWorkerToLeader spawns a remote agent on the worker
// that writes a file, then verifies it syncs back to the leader. This
// exercises the realistic flow: agent writes → worker fsnotify → gRPC
// stream → leader applies update.
func TestCluster_FileSyncWorkerToLeader(t *testing.T) {
	// Ensure the writer agent definition exists on the leader.
	agentMD := `---
name: sync-writer-agent
allowed_tools: [Bash, Write]
---

You are a test agent. Write files as instructed. Be concise.`
	apiWriteFile(t, "agents/sync-writer-agent/agent.md", agentMD)

	// Wait for the agent definition to sync to the worker.
	waitForWorkerFile(t, "/home/hiro/agents/sync-writer-agent/agent.md", 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx)
	defer cs.close()

	marker := fmt.Sprintf("w2l-sync-%d", time.Now().UnixNano())
	prompt := fmt.Sprintf(`Use ListNodes to find a non-home worker node, then SpawnInstance "sync-writer-agent" on it in ephemeral mode with this prompt:

"Use the Write tool to create workspace/sync-w2l-test.txt with exactly this content: %s"

Set the node parameter to the worker node's ID. Tell me the result.`, marker)

	resp := cs.chat(ctx, prompt)
	t.Logf("operator response: %s", resp)

	// Verify the file synced back to the leader (via the API).
	var leaderContent string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		content, err := apiReadFile("workspace/sync-w2l-test.txt")
		if err == nil && strings.Contains(content, marker) {
			leaderContent = content
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !strings.Contains(leaderContent, marker) {
		// Also check via docker exec for debugging.
		out, _ := exec.Command("docker", "exec", leaderContainer, "cat", "/home/hiro/workspace/sync-w2l-test.txt").CombinedOutput()
		wout, _ := exec.Command("docker", "exec", workerContainer, "cat", "/home/hiro/workspace/sync-w2l-test.txt").CombinedOutput()
		t.Errorf("worker→leader sync failed: leader has %q, want marker %q\nleader docker exec: %s\nworker docker exec: %s",
			leaderContent, marker, out, wout)
	}
}

// TestCluster_AgentDefinitionSyncsToWorker writes an agent definition on
// the leader via the API and verifies it appears on the worker node.
func TestCluster_AgentDefinitionSyncsToWorker(t *testing.T) {
	agentMD := `---
name: remote-worker-agent
allowed_tools: [Bash, Read, Write]
---

You are a test agent. When asked, run the command given and report the output. Be concise.`

	apiWriteFile(t, "agents/remote-worker-agent/agent.md", agentMD)

	waitForWorkerFile(t, "/home/hiro/agents/remote-worker-agent/agent.md", 15*time.Second)
}

// TestCluster_SpawnAgentOnWorkerNode is the core clustering test.
// It asks the operator to spawn an agent on the worker node, has
// that agent execute a bash command (which runs on the worker), and
// verifies the result comes back through the leader.
func TestCluster_SpawnAgentOnWorkerNode(t *testing.T) {
	// Ensure the agent definition exists on the leader.
	agentMD := `---
name: remote-exec-agent
allowed_tools: [Bash, Read, Write]
---

You are a test agent running on a remote node. Execute commands as asked. Be concise.`

	apiWriteFile(t, "agents/remote-exec-agent/agent.md", agentMD)

	// Wait for definition to sync to worker.
	waitForWorkerFile(t, "/home/hiro/agents/remote-exec-agent/agent.md", 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx)
	defer cs.close()

	// Ask the operator to spawn an ephemeral agent on the worker node.
	prompt := `I need you to do something on a remote worker node.

1. First use ListNodes to see what's available.
2. Pick the non-home node.
3. Use SpawnInstance to run the "remote-exec-agent" on that node in ephemeral mode.
   The prompt should be: "Run 'hostname' using bash and report the output."
4. Tell me the hostname that was reported.

Important: when calling SpawnInstance, set the "node_id" parameter to the worker node's ID.`

	resp := cs.chat(ctx, prompt)
	t.Logf("operator response: %s", resp)

	if resp == "" {
		t.Fatal("empty response from operator")
	}
	if len(resp) < 5 {
		t.Errorf("response too short, expected hostname output: %q", resp)
	}
}

// TestCluster_RemoteAgentWritesFile spawns an agent on the worker that
// writes a file, then verifies the file exists on the worker AND syncs
// back to the leader.
func TestCluster_RemoteAgentWritesFile(t *testing.T) {
	agentMD := `---
name: remote-writer-agent
allowed_tools: [Bash, Write]
---

You are a test agent. Write files as instructed. Use relative paths from your working directory. Be concise.`

	apiWriteFile(t, "agents/remote-writer-agent/agent.md", agentMD)

	// Wait for sync.
	waitForWorkerFile(t, "/home/hiro/agents/remote-writer-agent/agent.md", 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx)
	defer cs.close()

	marker := fmt.Sprintf("cluster-e2e-%d", time.Now().UnixNano())

	// Use a relative path (workspace/remote-test.txt) so the agent writes
	// relative to its working directory (/hiro on the worker).
	prompt := fmt.Sprintf(`Use ListNodes to find a non-home worker node, then SpawnInstance "remote-writer-agent" on it in ephemeral mode. The prompt should be:

"Use Write to create workspace/remote-test.txt with exactly this content: %s"

Set the node parameter to the worker node's ID. Tell me when done.`, marker)

	resp := cs.chat(ctx, prompt)
	t.Logf("operator response: %s", resp)

	// Verify the file exists on the worker.
	var workerContent string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("docker", "exec", workerContainer, "cat", "/home/hiro/workspace/remote-test.txt").Output()
		if err == nil && strings.Contains(string(out), marker) {
			workerContent = strings.TrimSpace(string(out))
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !strings.Contains(workerContent, marker) {
		// Debug: list files on worker.
		ls, _ := exec.Command("docker", "exec", workerContainer, "find", "/home/hiro/workspace", "-type", "f").CombinedOutput()
		t.Errorf("file on worker should contain marker %q, got %q\nworker workspace files: %s", marker, workerContent, ls)
	}

	// Verify the file syncs back to the leader via the API.
	var leaderContent string
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		content, err := apiReadFile("workspace/remote-test.txt")
		if err == nil && strings.Contains(content, marker) {
			leaderContent = content
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !strings.Contains(leaderContent, marker) {
		t.Errorf("file should sync back to leader with marker %q, got %q", marker, leaderContent)
	}
}

// TestCluster_RemoteAgentComputesResult spawns an agent on the worker
// that runs a deterministic computation and verifies the exact result
// comes back through the leader. This is the strongest test that remote
// tool execution actually works end-to-end.
func TestCluster_RemoteAgentComputesResult(t *testing.T) {
	agentMD := `---
name: remote-compute-agent
allowed_tools: [Bash]
---

You are a test agent. Run the exact command given to you. Report ONLY the raw output, nothing else.`

	apiWriteFile(t, "agents/remote-compute-agent/agent.md", agentMD)
	waitForWorkerFile(t, "/home/hiro/agents/remote-compute-agent/agent.md", 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx)
	defer cs.close()

	// Use a unique marker so we can verify the exact hash.
	marker := fmt.Sprintf("cluster-compute-%d", time.Now().UnixNano())

	// Compute the expected hash in Go (avoids sha256sum availability on macOS).
	h := sha256.Sum256([]byte(marker))
	expectedHash := fmt.Sprintf("%x", h)

	prompt := fmt.Sprintf(`Use ListNodes to find a non-home worker node, then SpawnInstance "remote-compute-agent" on that node in ephemeral mode. Set the node parameter to the worker's ID.

The prompt should be exactly: "Run this bash command and report ONLY the output: echo -n %s | sha256sum | cut -d' ' -f1"

Reply with ONLY the hash output, nothing else.`, marker)

	resp := cs.chat(ctx, prompt)
	t.Logf("operator response: %s", resp)

	if !strings.Contains(resp, expectedHash) {
		t.Errorf("expected response to contain hash %s, got: %s", expectedHash, resp)
	}
}

// --- WebSocket chat client ---

type chatMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chatSession struct {
	conn *websocket.Conn
	t    *testing.T
}

func openChat(t *testing.T, ctx context.Context) *chatSession {
	t.Helper()
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1) + "/ws/chat"

	host := strings.TrimPrefix(baseURL, "http://")
	host = strings.TrimPrefix(host, "https://")

	headers := http.Header{"Origin": {"http://" + host}}
	if httpClient != nil && httpClient.Jar != nil {
		if u, err := url.Parse(baseURL); err == nil {
			for _, c := range httpClient.Jar.Cookies(u) {
				headers.Add("Cookie", c.String())
			}
		}
	}

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("dial chat: %v", err)
	}
	conn.SetReadLimit(1 << 20) // 1MB
	return &chatSession{conn: conn, t: t}
}

func (c *chatSession) send(ctx context.Context, text string) {
	c.t.Helper()
	if err := wsjson.Write(ctx, c.conn, chatMessage{Type: "message", Content: text}); err != nil {
		c.t.Fatalf("sending message: %v", err)
	}
}

func (c *chatSession) readResponse(ctx context.Context) string {
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
		case "done":
			return sb.String()
		case "error":
			c.t.Fatalf("server error: %s", msg.Content)
		case "system":
			return msg.Content
		}
	}
}

func (c *chatSession) chat(ctx context.Context, text string) string {
	c.t.Helper()
	c.send(ctx, text)
	return c.readResponse(ctx)
}

func (c *chatSession) close() {
	c.conn.Close(websocket.StatusNormalClosure, "")
}

// --- Helpers ---

func waitHealthy(ctx context.Context) error {
	var lastErr error
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out; last error: %v", lastErr)
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

	// HIRO_MODEL may use "provider/model" format (e.g. "openrouter/x-ai/grok-4.1-fast").
	// The setup API expects provider and model separately, so strip the provider prefix.
	if prefix := provider + "/"; strings.HasPrefix(model, prefix) {
		model = strings.TrimPrefix(model, prefix)
	}

	body, _ := json.Marshal(map[string]string{
		"password":      "e2e-cluster-test-12345",
		"mode":          "leader",
		"node_name":     "e2e-leader",
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
	if resp.StatusCode == http.StatusConflict {
		return nil // already configured from a previous run
	}
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /api/setup: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// approveFirstPendingNode polls the pending nodes endpoint until a node
// appears, then approves it. This replaces the old join-token flow.
func approveFirstPendingNode(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for pending node")
		}
		resp, err := httpClient.Get(baseURL + "/api/cluster/pending")
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			time.Sleep(time.Second)
			continue
		}
		var nodes []struct {
			NodeID string `json:"node_id"`
			Name   string `json:"name"`
		}
		json.NewDecoder(resp.Body).Decode(&nodes)
		resp.Body.Close()

		if len(nodes) == 0 {
			time.Sleep(time.Second)
			continue
		}

		// Approve the first pending node.
		nodeID := nodes[0].NodeID
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/cluster/pending/%s/approve", baseURL, nodeID), nil)
		approveResp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("approving node %s: %w", nodeID, err)
		}
		approveResp.Body.Close()
		if approveResp.StatusCode != 200 {
			return fmt.Errorf("approving node %s: status %d", nodeID, approveResp.StatusCode)
		}
		fmt.Printf("approved node %s (%s)\n", nodes[0].Name, nodeID[:16]+"...")
		return nil
	}
}

func waitForOperator(ctx context.Context) (string, error) {
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
		req, _ := http.NewRequestWithContext(reqCtx, "GET", baseURL+"/api/instances", nil)
		resp, err := httpClient.Do(req)
		reqCancel()
		if err != nil {
			fmt.Printf("  operator poll error: %v\n", err)
			time.Sleep(time.Second)
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			time.Sleep(time.Second)
			continue
		}
		var instances []struct {
			ID       string `json:"id"`
			Mode     string `json:"mode"`
			ParentID string `json:"parent_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&instances); err == nil {
			for _, inst := range instances {
				if inst.ParentID == "" && inst.Mode == "persistent" {
					resp.Body.Close()
					return inst.ID, nil
				}
			}
		}
		resp.Body.Close()
		time.Sleep(time.Second)
	}
}

// apiWriteFile writes a file on the leader through the Hiro REST API.
// This ensures the hiro process creates the file, triggering fsnotify
// for the file sync watcher.
func apiWriteFile(t *testing.T, relPath, content string) {
	t.Helper()
	reqURL := fmt.Sprintf("%s/api/files/file?path=%s", baseURL, url.QueryEscape(relPath))
	req, err := http.NewRequest("PUT", reqURL, strings.NewReader(content))
	if err != nil {
		t.Fatalf("creating PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", relPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT %s: status %d: %s", relPath, resp.StatusCode, body)
	}
}

// apiReadFile reads a file from the leader through the Hiro REST API.
func apiReadFile(relPath string) (string, error) {
	reqURL := fmt.Sprintf("%s/api/files/file?path=%s", baseURL, url.QueryEscape(relPath))
	resp, err := httpClient.Get(reqURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", relPath, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// waitForWorkerFile polls until a file exists on the worker container.
func waitForWorkerFile(t *testing.T, absPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := exec.Command("docker", "exec", workerContainer, "test", "-f", absPath).Run()
		if err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("file %s did not appear on worker within %v", absPath, timeout)
}
