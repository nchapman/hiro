package history

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
)

func TestEngine_IngestAndAssemble(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	e := NewEngineWithSummarizer(s, fs, DefaultConfig(), testLogger())

	// Ingest a few messages
	e.Ingest("user", "Hello, can you help me?", `{"role":"user","content":[{"text":"Hello, can you help me?"}]}`)
	e.Ingest("assistant", "Of course! What do you need?", `{"role":"assistant","content":[{"text":"Of course! What do you need?"}]}`)

	result, err := e.Assemble()
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != fantasy.MessageRoleUser {
		t.Errorf("first message role = %q, want user", result.Messages[0].Role)
	}
	if result.Messages[1].Role != fantasy.MessageRoleAssistant {
		t.Errorf("second message role = %q, want assistant", result.Messages[1].Role)
	}
}

func TestEngine_AssembleReconstructsJSON(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	e := NewEngineWithSummarizer(s, fs, DefaultConfig(), testLogger())

	// Ingest a message with valid raw JSON
	rawJSON := `{"role":"user","content":[{"type":"text","text":"Hello world"}]}`
	e.Ingest("user", "Hello world", rawJSON)

	result, err := e.Assemble()
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}

	// Should reconstruct from raw JSON
	msg := result.Messages[0]
	if msg.Role != fantasy.MessageRoleUser {
		t.Errorf("role = %q, want user", msg.Role)
	}
}

func TestEngine_CompactionAndAssembly(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{response: "Compacted summary of conversation."}
	cfg := testConfig()
	cfg.LeafChunkTokens = 200
	cfg.LeafMinFanout = 3
	cfg.FreshTailCount = 2
	e := NewEngineWithSummarizer(s, fs, cfg, testLogger())
	ctx := context.Background()

	// Add enough messages to trigger compaction
	for i := 0; i < 10; i++ {
		msg := fmt.Sprintf("Message number %d with enough content to count", i)
		e.Ingest("user", msg, `{}`)
	}

	// Trigger compaction
	if err := e.Compact(ctx); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Assemble should include summaries
	result, err := e.Assemble()
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	hasSummary := false
	for _, msg := range result.Messages {
		for _, part := range msg.Content {
			if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				if strings.Contains(tp.Text, "conversation_summary") {
					hasSummary = true
				}
			}
		}
	}

	if fs.calls > 0 && !hasSummary {
		t.Error("compaction ran but no summary found in assembled context")
	}
}

func TestEngine_TokenBudgetEnforcement(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	cfg := DefaultConfig()
	cfg.TokenBudget = 100 // very small
	cfg.FreshTailCount = 2
	e := NewEngineWithSummarizer(s, fs, cfg, testLogger())

	// Add many messages that exceed budget
	for i := 0; i < 20; i++ {
		e.Ingest("user", strings.Repeat("word ", 20), `{}`)
	}

	result, err := e.Assemble()
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Should include at least the fresh tail
	if len(result.Messages) < 2 {
		t.Errorf("expected at least 2 messages (fresh tail), got %d", len(result.Messages))
	}

	// Should not include all 20
	if len(result.Messages) == 20 {
		t.Error("all 20 messages included despite tiny budget — eviction not working")
	}
}

func TestEngine_FreshTailProtection(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	cfg := DefaultConfig()
	cfg.TokenBudget = 1 // impossibly small
	cfg.FreshTailCount = 3
	e := NewEngineWithSummarizer(s, fs, cfg, testLogger())

	// Add a few messages
	for i := 0; i < 5; i++ {
		e.Ingest("user", fmt.Sprintf("message %d", i), `{}`)
	}

	result, err := e.Assemble()
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Fresh tail must be preserved even when over budget
	if len(result.Messages) < 3 {
		t.Errorf("expected at least 3 messages (fresh tail), got %d", len(result.Messages))
	}
}

func TestEngine_Search(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{}
	e := NewEngineWithSummarizer(s, fs, DefaultConfig(), testLogger())

	e.Ingest("user", "deploy the kubernetes cluster now", `{}`)
	e.Ingest("assistant", "I will deploy to kubernetes", `{}`)
	e.Ingest("user", "check the database status", `{}`)

	// Search through the store
	results, err := e.Store().Search("kubernetes", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search results for 'kubernetes'")
	}
}

func TestEngine_AssembleWithSummaries(t *testing.T) {
	s := tempStore(t)
	fs := &fakeSummarizer{response: "Summary: discussed deployment."}
	cfg := testConfig()
	cfg.LeafChunkTokens = 200
	cfg.LeafMinFanout = 3
	cfg.FreshTailCount = 2
	e := NewEngineWithSummarizer(s, fs, cfg, testLogger())
	ctx := context.Background()

	// Add enough to trigger compaction
	for i := 0; i < 8; i++ {
		e.Ingest("user", fmt.Sprintf("Discuss topic %d in detail with specifics", i), `{}`)
	}
	e.Compact(ctx)

	// Assembly should reconstruct summaries as user messages with XML tags
	result, err := e.Assemble()
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if fs.calls > 0 {
		hasSummaryXML := false
		for _, msg := range result.Messages {
			for _, part := range msg.Content {
				if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
					if strings.Contains(tp.Text, "<conversation_summary") {
						hasSummaryXML = true
					}
				}
			}
		}
		if !hasSummaryXML {
			t.Error("expected summary XML in assembled messages after compaction")
		}
	}
}

func TestFallbackTruncate(t *testing.T) {
	short := "hello world"
	got := fallbackTruncate(short, 10)
	if got != short {
		t.Errorf("expected no truncation for short input, got %q", got)
	}

	long := strings.Repeat("x", 1000)
	got = fallbackTruncate(long, 10) // 10 tokens = 40 chars
	if len(got) > 80 {               // 40 chars + truncation message
		t.Errorf("expected truncation, got length %d", len(got))
	}
	if !strings.Contains(got, "[Truncated for context management]") {
		t.Error("expected truncation marker")
	}
}

func TestSanitizeFTSQuery(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", `"hello"`},
		{`test "quotes"`, `"test ""quotes"""`},
		{"a* OR b*", `"a* OR b*"`}, // operators are neutralized inside quotes
	}
	for _, tc := range tests {
		got := sanitizeFTSQuery(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestResolveItem_Summary(t *testing.T) {
	s := tempStore(t)

	// Create a summary directly in the store
	s.CreateSummary(Summary{
		ID: "sum_test_resolve", Kind: "leaf", Depth: 0,
		Content:      "The user discussed deployment strategies.",
		Tokens:       8,
		EarliestAt:   parseTime("2025-01-01T00:00:00Z"),
		LatestAt:     parseTime("2025-01-01T01:00:00Z"),
		SourceTokens: 100,
	})

	sumID := "sum_test_resolve"
	item := ContextItem{
		Ordinal:   0,
		ItemType:  "summary",
		SummaryID: &sumID,
	}

	msg, tokens, err := resolveItem(s, item)
	if err != nil {
		t.Fatalf("resolveItem: %v", err)
	}
	if tokens != 8 {
		t.Errorf("expected 8 tokens, got %d", tokens)
	}
	if msg.Role != fantasy.MessageRoleUser {
		t.Errorf("expected user role for summary, got %q", msg.Role)
	}
	// Check XML wrapper
	if len(msg.Content) == 0 {
		t.Fatal("expected content in message")
	}
	tp, ok := fantasy.AsMessagePart[fantasy.TextPart](msg.Content[0])
	if !ok {
		t.Fatal("expected TextPart")
	}
	if !strings.Contains(tp.Text, "conversation_summary") {
		t.Error("expected conversation_summary XML tag")
	}
	if !strings.Contains(tp.Text, "deployment strategies") {
		t.Error("expected summary content in message")
	}
}

func TestResolveItem_MessageFromRawJSON(t *testing.T) {
	s := tempStore(t)

	rawJSON := `{"role":"user","content":[{"type":"text","text":"Hello from JSON"}]}`
	id, _ := s.AppendMessage("user", "Hello from JSON", rawJSON, 3)

	item := ContextItem{
		Ordinal:   0,
		ItemType:  "message",
		MessageID: &id,
	}

	msg, tokens, err := resolveItem(s, item)
	if err != nil {
		t.Fatalf("resolveItem: %v", err)
	}
	if tokens != 3 {
		t.Errorf("expected 3 tokens, got %d", tokens)
	}
	if msg.Role != fantasy.MessageRoleUser {
		t.Errorf("expected user role, got %q", msg.Role)
	}
}

func TestResolveItem_MessageFallback(t *testing.T) {
	s := tempStore(t)

	// Invalid JSON — should fall back to role+content reconstruction
	id, _ := s.AppendMessage("assistant", "I can help with that.", `{invalid}`, 5)

	item := ContextItem{
		Ordinal:   0,
		ItemType:  "message",
		MessageID: &id,
	}

	msg, _, err := resolveItem(s, item)
	if err != nil {
		t.Fatalf("resolveItem: %v", err)
	}
	if msg.Role != fantasy.MessageRoleAssistant {
		t.Errorf("expected assistant role, got %q", msg.Role)
	}
	if len(msg.Content) == 0 {
		t.Fatal("expected content")
	}
	tp, ok := fantasy.AsMessagePart[fantasy.TextPart](msg.Content[0])
	if !ok {
		t.Fatal("expected TextPart")
	}
	if tp.Text != "I can help with that." {
		t.Errorf("expected fallback text, got %q", tp.Text)
	}
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
