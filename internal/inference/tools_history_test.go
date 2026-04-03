package inference

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

func seedHistorySession(t *testing.T, pdb *platformdb.DB) string {
	t.Helper()
	sessionID := "session-hist"
	createTestSession(t, pdb, sessionID)
	return sessionID
}

func runHistoryTool(t *testing.T, tools []Tool, name, input string) fantasy.ToolResponse {
	t.Helper()
	for _, tool := range tools {
		if tool.Info().Name == name {
			resp, err := tool.Run(context.Background(), fantasy.ToolCall{
				ID:    "test-call",
				Name:  name,
				Input: input,
			})
			if err != nil {
				t.Fatalf("unexpected error from %s: %v", name, err)
			}
			return resp
		}
	}
	t.Fatalf("tool %q not found", name)
	return fantasy.ToolResponse{}
}

func TestBuildHistoryTools_ReturnsTwoTools(t *testing.T) {
	pdb := openTestDB(t)
	tools := buildHistoryTools(pdb, "session-1")
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Info().Name] = true
	}
	if !names["HistorySearch"] || !names["HistoryRecall"] {
		t.Errorf("expected HistorySearch and HistoryRecall, got %v", names)
	}
}

func TestHistorySearch_EmptyQuery(t *testing.T) {
	pdb := openTestDB(t)
	tools := buildHistoryTools(pdb, "session-1")

	resp := runHistoryTool(t, tools, "HistorySearch", `{"query":""}`)
	if !resp.IsError {
		t.Error("expected error for empty query")
	}
}

func TestHistorySearch_NoResults(t *testing.T) {
	pdb := openTestDB(t)
	sessionID := seedHistorySession(t, pdb)
	tools := buildHistoryTools(pdb, sessionID)

	resp := runHistoryTool(t, tools, "HistorySearch", `{"query":"nonexistent"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "No results") {
		t.Errorf("expected 'No results', got: %s", resp.Content)
	}
}

func TestHistorySearch_FindsMessages(t *testing.T) {
	pdb := openTestDB(t)
	sessionID := seedHistorySession(t, pdb)
	ctx := context.Background()

	// Insert a message with searchable content.
	_, err := pdb.AppendMessage(ctx, sessionID, "user", "How do I deploy to production?", "", 10)
	if err != nil {
		t.Fatal(err)
	}

	tools := buildHistoryTools(pdb, sessionID)

	resp := runHistoryTool(t, tools, "HistorySearch", `{"query":"deploy","scope":"messages"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Found") {
		t.Errorf("expected results, got: %s", resp.Content)
	}
}

func TestHistorySearch_DefaultScope(t *testing.T) {
	pdb := openTestDB(t)
	sessionID := seedHistorySession(t, pdb)
	ctx := context.Background()

	_, err := pdb.AppendMessage(ctx, sessionID, "user", "Tell me about golang testing", "", 10)
	if err != nil {
		t.Fatal(err)
	}

	tools := buildHistoryTools(pdb, sessionID)

	// No scope = default "all".
	resp := runHistoryTool(t, tools, "HistorySearch", `{"query":"golang testing"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Found") {
		t.Errorf("expected results, got: %s", resp.Content)
	}
}

func TestHistorySearch_SummaryScope(t *testing.T) {
	pdb := openTestDB(t)
	sessionID := seedHistorySession(t, pdb)
	ctx := context.Background()

	now := time.Now()
	if err := pdb.CreateSummary(ctx, platformdb.Summary{
		ID:           "sum-1",
		SessionID:    sessionID,
		Kind:         "leaf",
		Depth:        0,
		Content:      "Summary about kubernetes deployment",
		Tokens:       50,
		EarliestAt:   now.Add(-time.Hour),
		LatestAt:     now,
		SourceTokens: 200,
	}); err != nil {
		t.Fatal(err)
	}

	tools := buildHistoryTools(pdb, sessionID)

	resp := runHistoryTool(t, tools, "HistorySearch", `{"query":"kubernetes deployment","scope":"summaries"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Found") {
		t.Errorf("expected results, got: %s", resp.Content)
	}
}

// --- HistoryRecall tests ---

func TestHistoryRecall_EmptyID(t *testing.T) {
	pdb := openTestDB(t)
	tools := buildHistoryTools(pdb, "session-1")

	resp := runHistoryTool(t, tools, "HistoryRecall", `{"summary_id":""}`)
	if !resp.IsError {
		t.Error("expected error for empty summary_id")
	}
}

func TestHistoryRecall_NotFound(t *testing.T) {
	pdb := openTestDB(t)
	tools := buildHistoryTools(pdb, "session-1")

	resp := runHistoryTool(t, tools, "HistoryRecall", `{"summary_id":"sum-nonexistent"}`)
	if !resp.IsError {
		t.Error("expected error for nonexistent summary")
	}
	if !strings.Contains(resp.Content, "not found") {
		t.Errorf("expected 'not found' in error, got: %s", resp.Content)
	}
}

func TestHistoryRecall_LeafSummary(t *testing.T) {
	pdb := openTestDB(t)
	sessionID := seedHistorySession(t, pdb)
	ctx := context.Background()

	now := time.Now().Truncate(time.Minute)
	if err := pdb.CreateSummary(ctx, platformdb.Summary{
		ID:           "sum-leaf-1",
		SessionID:    sessionID,
		Kind:         "leaf",
		Depth:        0,
		Content:      "Discussion about deployment strategy.",
		Tokens:       30,
		EarliestAt:   now.Add(-time.Hour),
		LatestAt:     now,
		SourceTokens: 200,
	}); err != nil {
		t.Fatal(err)
	}

	// Add source messages.
	msgID, err := pdb.AppendMessage(ctx, sessionID, "user", "How should we deploy?", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := pdb.LinkSummaryMessages(ctx, "sum-leaf-1", []int64{msgID}); err != nil {
		t.Fatal(err)
	}

	tools := buildHistoryTools(pdb, sessionID)

	resp := runHistoryTool(t, tools, "HistoryRecall", `{"summary_id":"sum-leaf-1"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "deployment strategy") {
		t.Error("expected summary content in response")
	}
	if !strings.Contains(resp.Content, "Source Messages") {
		t.Error("expected source messages section for leaf summary")
	}
	if !strings.Contains(resp.Content, "How should we deploy") {
		t.Error("expected source message content")
	}
}

func TestHistoryRecall_CondensedSummary(t *testing.T) {
	pdb := openTestDB(t)
	sessionID := seedHistorySession(t, pdb)
	ctx := context.Background()

	now := time.Now().Truncate(time.Minute)

	// Create child summaries.
	if err := pdb.CreateSummary(ctx, platformdb.Summary{
		ID:           "sum-child-1",
		SessionID:    sessionID,
		Kind:         "leaf",
		Depth:        0,
		Content:      "Child summary about setup.",
		Tokens:       20,
		EarliestAt:   now.Add(-2 * time.Hour),
		LatestAt:     now.Add(-time.Hour),
		SourceTokens: 100,
	}); err != nil {
		t.Fatal(err)
	}

	// Create condensed parent.
	if err := pdb.CreateSummary(ctx, platformdb.Summary{
		ID:           "sum-parent-1",
		SessionID:    sessionID,
		Kind:         "condensed",
		Depth:        1,
		Content:      "High-level overview.",
		Tokens:       15,
		EarliestAt:   now.Add(-2 * time.Hour),
		LatestAt:     now,
		SourceTokens: 100,
	}); err != nil {
		t.Fatal(err)
	}

	if err := pdb.LinkSummaryParents(ctx, "sum-parent-1", []string{"sum-child-1"}); err != nil {
		t.Fatal(err)
	}

	tools := buildHistoryTools(pdb, sessionID)

	resp := runHistoryTool(t, tools, "HistoryRecall", `{"summary_id":"sum-parent-1"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "High-level overview") {
		t.Error("expected parent summary content")
	}
	if !strings.Contains(resp.Content, "Child Summaries") {
		t.Error("expected child summaries section for condensed summary")
	}
	if !strings.Contains(resp.Content, "Child summary about setup") {
		t.Error("expected child summary content")
	}
}

func TestHistoryRecall_LeafNoSourceMessages(t *testing.T) {
	pdb := openTestDB(t)
	sessionID := seedHistorySession(t, pdb)
	ctx := context.Background()

	now := time.Now().Truncate(time.Minute)
	if err := pdb.CreateSummary(ctx, platformdb.Summary{
		ID:           "sum-orphan",
		SessionID:    sessionID,
		Kind:         "leaf",
		Depth:        0,
		Content:      "Orphan summary with no sources.",
		Tokens:       10,
		EarliestAt:   now.Add(-time.Hour),
		LatestAt:     now,
		SourceTokens: 50,
	}); err != nil {
		t.Fatal(err)
	}

	tools := buildHistoryTools(pdb, sessionID)

	resp := runHistoryTool(t, tools, "HistoryRecall", `{"summary_id":"sum-orphan"}`)
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	// Should not crash, just omit source section.
	if strings.Contains(resp.Content, "Source Messages") {
		t.Error("should not show Source Messages when there are none")
	}
}
