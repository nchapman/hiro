//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestE2E_Todos(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cs := openChat(t, ctx, "")
	defer cs.close()

	resp := cs.chat(ctx, "Use the TodoWrite tool right now to create a todo list with these 3 tasks: design schema, implement endpoints, write tests. Call the TodoWrite tool — do NOT just write text.")
	t.Logf("Todo response: %s", resp)

	// Debug: list all session directories and find todos.yaml anywhere.
	sessDir := activeSessionDir(t, operatorID)
	t.Logf("Active session dir: %s", sessDir)

	// Search for todos.yaml anywhere under the instance.
	instDir := instanceDir(t, operatorID)
	findOut := containerExec(t, "find", instDir, "-name", "todos.yaml")
	t.Logf("Found todos.yaml at: %s", findOut)

	todosPath := sessDir + "/todos.yaml"
	if !containerFileExists(t, todosPath) {
		// If todos.yaml exists in a different session, note it.
		if findOut != "" {
			t.Logf("todos.yaml found in different location: %s", strings.TrimSpace(findOut))
		}

		t.Log("todos.yaml not found on first attempt, retrying with explicit prompt")
		resp = cs.chat(ctx, "You did not call the TodoWrite tool. Please call it now. The tool takes a 'todos' parameter with an array of objects like {content: 'task name', status: 'pending'}. Call it immediately.")
		t.Logf("Retry response: %s", resp)

		// Re-check — session might have changed.
		findOut = containerExec(t, "find", instDir, "-name", "todos.yaml")
		t.Logf("After retry, found todos.yaml at: %s", findOut)

		if !containerFileExists(t, todosPath) && findOut != "" {
			// Use wherever it was found.
			todosPath = strings.TrimSpace(strings.Split(findOut, "\n")[0])
			t.Logf("Using alternative path: %s", todosPath)
		}

		if !containerFileExists(t, todosPath) {
			t.Fatal("expected todos.yaml to be created after retry")
		}
	}

	content := containerExec(t, "cat", todosPath)

	// Check that all three tasks appear.
	lower := strings.ToLower(content)
	for _, task := range []string{"schema", "endpoint", "test"} {
		if !strings.Contains(lower, task) {
			t.Errorf("expected %q in todos.yaml, got:\n%s", task, content)
		}
	}
	t.Logf("Todos:\n%s", content)
}
