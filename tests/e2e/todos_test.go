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

	resp := cs.chat(ctx, "Create a todo list with exactly 3 tasks for building a REST API: design schema, implement endpoints, write tests. Use the todos tool now.")
	t.Logf("Todo response: %s", resp)

	// Verify todos.yaml was created in the operator's active session dir.
	sessDir := activeSessionDir(t, operatorID)
	content := containerExec(t, "cat", sessDir+"/todos.yaml")
	if content == "" {
		t.Fatal("expected todos.yaml to be created, got empty")
	}

	// Check that all three tasks appear.
	lower := strings.ToLower(content)
	for _, task := range []string{"schema", "endpoint", "test"} {
		if !strings.Contains(lower, task) {
			t.Errorf("expected %q in todos.yaml, got:\n%s", task, content)
		}
	}
	t.Logf("Todos:\n%s", content)
}
