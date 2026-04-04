//go:build e2e

package e2e

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestE2E_MemoryInjection(t *testing.T) {
	// Write a memory into the operator's instance dir (instance-level state).
	instDir := instanceDir(t, operatorID)
	memPath := instDir + "/memory.md"

	// Capture original content so we can restore it after the test.
	origCmd := exec.Command("docker", "exec", containerName, "cat", memPath)
	origContent, _ := origCmd.Output() // may not exist yet, that's fine
	t.Cleanup(func() {
		containerWriteFile(t, memPath, string(origContent))
	})

	containerWriteFile(t, memPath, "The user's favorite color is blue.")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	resp := cs.chat(ctx, "What is my favorite color? Just say the color.")
	if !strings.Contains(strings.ToLower(resp), "blue") {
		t.Errorf("expected 'blue' from memory, got %q", resp)
	}
	t.Logf("Memory response: %s", resp)
}

func TestE2E_MemoryWriteTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	cs.chat(ctx, `Use the AddMemory tool with content "The project uses PostgreSQL 16". Do not use any other tools.`)

	// Verify memory.md was written at instance level.
	instDir := instanceDir(t, operatorID)
	content := containerExec(t, "cat", instDir+"/memory.md")
	if !strings.Contains(strings.ToLower(content), "postgresql") {
		t.Errorf("expected 'postgresql' in memory.md, got %q", content)
	}
	t.Logf("Memory written: %s", content)
}
