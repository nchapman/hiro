//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestE2E_BackgroundTaskCompletion verifies the full background task
// notification flow: an agent runs a command in the background, the
// control plane detects completion, and the agent receives a
// <task-notification> meta message triggering an auto-response.
func TestE2E_BackgroundTaskCompletion(t *testing.T) {
	// Create a persistent agent with bash + task tools.
	containerWriteFile(t, "/home/hiro/agents/bg-test/agent.md", `---
name: bg-test
allowed_tools: [Bash, TaskOutput, TaskStop]
---

You are a test agent. Be concise.
When you receive a task-notification about a completed background task, report the task ID and status.`)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	agentID := spawnPersistentAgent(t, ctx, "bg-test")
	t.Logf("spawned bg-test agent: %s", agentID)

	cs := openChat(t, ctx, agentID)
	defer cs.close()

	// Ask the agent to run a short command in the background.
	// The command sleeps briefly so it doesn't complete inline during
	// the 100ms fast-failure check, but finishes quickly enough for the test.
	resp := cs.chat(ctx, `Run this command in the background: sleep 2 && echo BACKGROUND_DONE. Set run_in_background to true and description to "bg test". Then tell me the task ID you received.`)
	t.Logf("initial response: %s", resp)

	lower := strings.ToLower(resp)
	if !strings.Contains(lower, "background") && !strings.Contains(lower, "id") {
		t.Errorf("expected response to mention background task ID, got: %s", resp)
	}

	// Now wait for the completion notification to arrive as an auto-triggered
	// meta turn. The notification triggers a new inference turn, which streams
	// deltas/done through the WebSocket.
	t.Log("waiting for completion notification...")
	notifResp, _ := cs.readResponse(ctx)
	t.Logf("notification response: %s", notifResp)

	// The agent should have reacted to the <task-notification> XML.
	notifLower := strings.ToLower(notifResp)
	if !strings.Contains(notifLower, "completed") && !strings.Contains(notifLower, "done") && !strings.Contains(notifLower, "task") {
		t.Errorf("expected notification response to mention completion, got: %s", notifResp)
	}
}

// TestE2E_BackgroundTaskOutput verifies that TaskOutput can retrieve
// output from a completed background task.
func TestE2E_BackgroundTaskOutput(t *testing.T) {
	// Use a persistent agent so we can do multi-turn interaction.
	containerWriteFile(t, "/home/hiro/agents/taskout-test/agent.md", `---
name: taskout-test
allowed_tools: [Bash, TaskOutput, TaskStop]
---

You are a test agent. Be concise. Follow instructions exactly.`)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	agentID := spawnPersistentAgent(t, ctx, "taskout-test")

	cs := openChat(t, ctx, agentID)
	defer cs.close()

	// Step 1: start background command.
	resp1 := cs.chat(ctx, `Run "echo MARKER_12345" using Bash with run_in_background set to true. Tell me the task ID.`)
	t.Logf("step 1: %s", resp1)

	// Step 2: retrieve output via TaskOutput (the task should be done by now).
	resp2 := cs.chat(ctx, `Use TaskOutput with that task ID to get the result. Tell me the output.`)
	t.Logf("step 2: %s", resp2)

	if !strings.Contains(resp2, "MARKER_12345") {
		t.Errorf("expected response to contain MARKER_12345, got: %s", resp2)
	}
}
