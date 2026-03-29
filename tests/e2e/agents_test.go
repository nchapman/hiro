//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestE2E_SpawnSubagent(t *testing.T) {
	// Pre-write a simple ephemeral agent definition.
	containerWriteFile(t, "/hive/agents/echo-test/agent.md", `---
name: echo-test
---

You are a concise test agent. Respond in one short sentence.`)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	resp := cs.chat(ctx, `Use spawn_instance to run the agent named "echo-test" with the prompt "What is the capital of France? One word." and tell me what it responded.`)
	if !strings.Contains(strings.ToLower(resp), "paris") {
		t.Errorf("expected 'paris' in response, got %q", resp)
	}
	t.Logf("Spawn response: %s", resp)
}

func TestE2E_CreateAgent(t *testing.T) {
	// Write the agent definition via docker exec (as root) since agents/
	// is not writable by agent UIDs in Docker — this mirrors an operator
	// pre-provisioning agent definitions.
	containerWriteFile(t, "/hive/agents/greeter/agent.md", `---
name: greeter
---

Always respond with exactly "HELLO WORLD" in all caps. Nothing else.`)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	resp := cs.chat(ctx, `Use spawn_instance with agent "greeter" and prompt "Say your greeting." and report back exactly what it said.`)
	t.Logf("Create agent response: %s", resp)

	if !strings.Contains(strings.ToUpper(resp), "HELLO WORLD") {
		t.Errorf("expected 'HELLO WORLD' in response, got %q", resp)
	}
}

func TestE2E_CreateSkill(t *testing.T) {
	// Pre-write the agent and skill via docker exec (as root) since agents/
	// is not writable by agent UIDs in Docker.
	containerWriteFile(t, "/hive/agents/responder/agent.md", `---
name: responder
---

You are a concise test agent. When you have skills, use them. Keep responses short.`)

	containerWriteFile(t, "/hive/agents/responder/skills/pirate-speak.md", `---
name: pirate-speak
description: Always respond in pirate speak using words like arr, matey, and ahoy.
---

When activated, respond entirely in pirate speak.`)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	resp := cs.chat(ctx, `Use spawn_instance with agent "responder" and prompt "Say hello like a pirate. Use your pirate-speak skill." and report back exactly what it said.`)
	t.Logf("Create skill response: %s", resp)

	lower := strings.ToLower(resp)
	hasPirate := strings.Contains(lower, "arr") ||
		strings.Contains(lower, "ahoy") ||
		strings.Contains(lower, "matey") ||
		strings.Contains(lower, "pirate")
	if !hasPirate {
		t.Errorf("expected pirate speak in response, got %q", resp)
	}
}
