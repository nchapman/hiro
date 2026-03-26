package inference

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hivebot/internal/platform/db"
)

func openTestDB(t *testing.T) *platformdb.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := platformdb.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func createTestSession(t *testing.T, pdb *platformdb.DB, id string) {
	t.Helper()
	if err := pdb.CreateSession(platformdb.Session{
		ID:        id,
		AgentName: "test-agent",
		Mode:      "persistent",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

func appendMsg(t *testing.T, pdb *platformdb.DB, sessionID, role, content string, tokens int) {
	t.Helper()
	msg := fantasy.NewUserMessage(content)
	if role == "assistant" {
		msg = fantasy.Message{
			Role:    fantasy.MessageRoleAssistant,
			Content: []fantasy.MessagePart{fantasy.TextPart{Text: content}},
		}
	}
	raw, _ := json.Marshal(msg)
	if _, err := pdb.AppendMessage(sessionID, role, content, string(raw), tokens); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
}

func TestAssemble_Empty(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	result, err := Assemble(pdb, "s1", DefaultCompactionConfig())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(result.Messages))
	}
}

func TestAssemble_ReturnsMessages(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	appendMsg(t, pdb, "s1", "user", "hello", 10)
	appendMsg(t, pdb, "s1", "assistant", "hi there", 15)
	appendMsg(t, pdb, "s1", "user", "how are you?", 12)

	result, err := Assemble(pdb, "s1", DefaultCompactionConfig())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	if result.EstimatedTokens != 37 {
		t.Errorf("estimated tokens = %d, want 37", result.EstimatedTokens)
	}

	// Verify message order.
	text0 := result.Messages[0].Content[0].(fantasy.TextPart).Text
	if text0 != "hello" {
		t.Errorf("first message = %q, want hello", text0)
	}
}

func TestAssemble_RespectsBudget(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	// Add many messages that exceed the token budget.
	for i := 0; i < 50; i++ {
		appendMsg(t, pdb, "s1", "user", "message content here", 5000)
	}

	cfg := CompactionConfig{
		TokenBudget:    20_000,
		FreshTailCount: 5,
	}
	result, err := Assemble(pdb, "s1", cfg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Fresh tail of 5 messages = 25,000 tokens (already over budget).
	// No evictable messages should be included.
	if result.EstimatedTokens > 25_000 {
		t.Errorf("tokens %d: only fresh tail should remain, max 25000", result.EstimatedTokens)
	}
	// Must include at least the fresh tail.
	if len(result.Messages) < 5 {
		t.Errorf("expected at least 5 fresh tail messages, got %d", len(result.Messages))
	}
}

func TestAssemble_SessionIsolation(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")
	createTestSession(t, pdb, "s2")

	appendMsg(t, pdb, "s1", "user", "session 1 msg", 10)
	appendMsg(t, pdb, "s2", "user", "session 2 msg", 10)

	result, err := Assemble(pdb, "s1", DefaultCompactionConfig())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message from s1, got %d", len(result.Messages))
	}
}
