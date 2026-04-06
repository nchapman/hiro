package inference

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
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
	if err := pdb.CreateSession(context.Background(), platformdb.Session{
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
	if _, err := pdb.AppendMessage(context.Background(), sessionID, role, content, string(raw), tokens); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
}

func TestAssemble_Empty(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	result, err := Assemble(context.Background(), pdb, "s1", DefaultCompactionConfig())
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

	result, err := Assemble(context.Background(), pdb, "s1", DefaultCompactionConfig())
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
	text0 := textPartText(t, result.Messages[0].Content[0])
	if text0 != "hello" {
		t.Errorf("first message = %q, want hello", text0)
	}
}

func TestAssemble_RespectsBudget(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	// Add many messages that exceed the token budget.
	for range 50 {
		appendMsg(t, pdb, "s1", "user", "message content here", 5000)
	}

	cfg := CompactionConfig{
		TokenBudget:    20_000,
		FreshTailCount: 5,
	}
	result, err := Assemble(context.Background(), pdb, "s1", cfg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Fresh tail is capped at 80% of budget (16,000 tokens).
	// With 5000 tokens per message, only 3 messages fit (15,000 <= 16,000).
	// Plus up to 4,000 remaining budget for evictable (not enough for 5000-token msg).
	if result.EstimatedTokens > cfg.TokenBudget {
		t.Errorf("tokens %d exceeds budget %d", result.EstimatedTokens, cfg.TokenBudget)
	}
	// Must include at least 1 message.
	if len(result.Messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(result.Messages))
	}
}

func TestAssemble_TailTruncation(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	// 10 messages, 3000 tokens each = 30,000 total.
	for range 10 {
		appendMsg(t, pdb, "s1", "user", "message content here", 3000)
	}

	cfg := CompactionConfig{
		TokenBudget:    10_000,
		FreshTailCount: 10, // wants all 10, but tail cap = 8000 tokens
	}
	result, err := Assemble(context.Background(), pdb, "s1", cfg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Tail cap = 80% of 10,000 = 8,000. Each msg = 3000.
	// Tail shrinks to 2 messages (6,000 tokens <= 8,000).
	// Remaining budget: 10,000 - 6,000 = 4,000. One more evictable (3,000) fits.
	// Total: 3 messages, 9,000 tokens.
	if result.EstimatedTokens > cfg.TokenBudget {
		t.Errorf("tokens %d exceeds budget %d", result.EstimatedTokens, cfg.TokenBudget)
	}
	if len(result.Messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(result.Messages))
	}
	// At least 1 message must always be kept even if it exceeds the cap.
	if len(result.Messages) == 0 {
		t.Fatal("no messages returned")
	}
}

func TestAssemble_SessionIsolation(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")
	createTestSession(t, pdb, "s2")

	appendMsg(t, pdb, "s1", "user", "session 1 msg", 10)
	appendMsg(t, pdb, "s2", "user", "session 2 msg", 10)

	result, err := Assemble(context.Background(), pdb, "s1", DefaultCompactionConfig())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message from s1, got %d", len(result.Messages))
	}
}

func TestResolveItem_MessageWithRawJSON(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")
	appendMsg(t, pdb, "s1", "user", "hello", 10)

	items, err := pdb.GetContextItems(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	msg, tokens, err := resolveItem(context.Background(), pdb, items[0])
	if err != nil {
		t.Fatalf("resolveItem: %v", err)
	}
	if tokens != 10 {
		t.Errorf("expected 10 tokens, got %d", tokens)
	}
	if msg.Role != fantasy.MessageRoleUser {
		t.Errorf("expected user role, got %s", msg.Role)
	}
}

func TestResolveItem_MessageWithoutRawJSON(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	// Insert a message without raw_json.
	if _, err := pdb.AppendMessage(context.Background(), "s1", "user", "plain text", "", 5); err != nil {
		t.Fatal(err)
	}

	items, err := pdb.GetContextItems(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}

	msg, tokens, err := resolveItem(context.Background(), pdb, items[0])
	if err != nil {
		t.Fatalf("resolveItem: %v", err)
	}
	if tokens != 5 {
		t.Errorf("expected 5 tokens, got %d", tokens)
	}
	if msg.Role != "user" {
		t.Errorf("expected user role, got %s", msg.Role)
	}
	// Should fallback to text content.
	if len(msg.Content) == 0 {
		t.Fatal("expected content parts")
	}
}

func TestResolveItem_SummaryItem(t *testing.T) {
	pdb := openTestDB(t)
	createTestSession(t, pdb, "s1")

	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)

	if err := pdb.CreateSummary(ctx, platformdb.Summary{
		ID:           "sum-1",
		SessionID:    "s1",
		Kind:         "leaf",
		Depth:        0,
		Content:      "Summary of the conversation.",
		Tokens:       50,
		EarliestAt:   now.Add(-time.Hour),
		LatestAt:     now,
		SourceTokens: 200,
	}); err != nil {
		t.Fatal(err)
	}

	// Replace the message context item with a summary context item.
	sumID := "sum-1"
	item := platformdb.ContextItem{
		SessionID: "s1",
		Ordinal:   0,
		ItemType:  "summary",
		SummaryID: &sumID,
	}

	msg, tokens, err := resolveItem(ctx, pdb, item)
	if err != nil {
		t.Fatalf("resolveItem summary: %v", err)
	}
	if tokens != 50 {
		t.Errorf("expected 50 tokens, got %d", tokens)
	}
	if msg.Role != fantasy.MessageRoleUser {
		t.Errorf("expected user role for summary, got %s", msg.Role)
	}
	text := textPartText(t, msg.Content[0])
	if !strings.Contains(text, "conversation_summary") {
		t.Error("expected conversation_summary tag in summary message")
	}
	if !strings.Contains(text, "Summary of the conversation.") {
		t.Error("expected summary content in message")
	}
}

func TestResolveItem_NilMessageID(t *testing.T) {
	item := platformdb.ContextItem{
		ItemType:  "message",
		MessageID: nil,
	}
	_, _, err := resolveItem(context.Background(), nil, item)
	if err == nil {
		t.Error("expected error for nil message_id")
	}
}

func TestResolveItem_NilSummaryID(t *testing.T) {
	item := platformdb.ContextItem{
		ItemType:  "summary",
		SummaryID: nil,
	}
	_, _, err := resolveItem(context.Background(), nil, item)
	if err == nil {
		t.Error("expected error for nil summary_id")
	}
}

func TestResolveItem_UnknownType(t *testing.T) {
	item := platformdb.ContextItem{
		ItemType: "unknown",
	}
	_, _, err := resolveItem(context.Background(), nil, item)
	if err == nil {
		t.Error("expected error for unknown item type")
	}
}
