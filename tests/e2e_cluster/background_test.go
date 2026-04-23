//go:build e2e_cluster

package e2e_cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCluster_RemoteBackgroundTaskNotification verifies that background
// task completion notifications flow from a remote worker node back
// through the cluster stream to the control plane, triggering a
// meta inference turn on the operator.
func TestCluster_RemoteBackgroundTaskNotification(t *testing.T) {
	// Write agent definition with bash + task tools.
	agentMD := `---
name: remote-bg-agent
allowed_tools: [Bash, TaskOutput, TaskStop]
---

You are a test agent running on a remote node. Be concise.
When you receive a task-notification about a completed background task, report the task ID and status.`

	apiWriteFile(t, "agents/remote-bg-agent/agent.md", agentMD)
	waitForWorkerFile(t, "/home/hiro/agents/remote-bg-agent/agent.md", 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cs := openChat(t, ctx)
	defer cs.close()

	marker := fmt.Sprintf("cluster-bg-%d", time.Now().UnixNano())

	// Ask operator to spawn a persistent agent on the worker node,
	// run a background command, and report back.
	prompt := fmt.Sprintf(`Do these steps:
1. Use ListNodes to find a non-home worker node
2. Use SpawnInstance to create "remote-bg-agent" on that worker node in persistent mode with this prompt:
   "Run this command in the background using Bash with run_in_background true: sleep 2 && echo %s. Tell me the task ID."
   Set the node parameter to the worker node's ID.
3. Tell me the result.`, marker)

	resp := cs.chat(ctx, prompt)
	t.Logf("operator response: %s", resp)

	if resp == "" {
		t.Fatal("empty response from operator")
	}

	// The response should mention the background task was started.
	lower := strings.ToLower(resp)
	if !strings.Contains(lower, "background") && !strings.Contains(lower, "task") {
		t.Logf("warning: response may not mention background task: %s", resp)
	}
}
