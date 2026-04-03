package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAppendMessage_Meta(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	id, err := d.AppendMessage(ctx, "s1", "system", "hidden note", "{}", 5, true)
	if err != nil {
		t.Fatalf("AppendMessage meta: %v", err)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !m.Meta {
		t.Error("expected Meta=true")
	}
}

func TestAppendMessage_SequenceNumbering(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	id1, _ := d.AppendMessage(ctx, "s1", "user", "first", "{}", 10)
	id2, _ := d.AppendMessage(ctx, "s1", "assistant", "second", "{}", 15)
	id3, _ := d.AppendMessage(ctx, "s1", "user", "third", "{}", 20)

	m1, _ := d.GetMessage(ctx, id1)
	m2, _ := d.GetMessage(ctx, id2)
	m3, _ := d.GetMessage(ctx, id3)

	if m1.Seq != 1 || m2.Seq != 2 || m3.Seq != 3 {
		t.Errorf("expected seq 1,2,3 got %d,%d,%d", m1.Seq, m2.Seq, m3.Seq)
	}
}

func TestGetMessage_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.GetMessage(context.Background(), 99999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetMessages_Empty(t *testing.T) {
	d := openTestDB(t)
	msgs, err := d.GetMessages(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetMessages(nil): %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil for empty IDs, got %+v", msgs)
	}

	msgs, err = d.GetMessages(context.Background(), []int64{})
	if err != nil {
		t.Fatalf("GetMessages([]): %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil for empty slice, got %+v", msgs)
	}
}

func TestRecentMessages_Ordering(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.AppendMessage(ctx, "s1", "user", "first", "{}", 10)
	d.AppendMessage(ctx, "s1", "assistant", "second", "{}", 10)
	d.AppendMessage(ctx, "s1", "user", "third", "{}", 10)

	// Limit 2 should return the 2 most recent, oldest first.
	msgs, err := d.RecentMessages(ctx, "s1", 2)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "second" || msgs[1].Content != "third" {
		t.Errorf("expected [second, third], got [%s, %s]", msgs[0].Content, msgs[1].Content)
	}
}

func TestRecentMessages_EmptySession(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	msgs, err := d.RecentMessages(ctx, "s1", 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestUpdateMessageTimestamp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	id, _ := d.AppendMessage(ctx, "s1", "user", "hello", "{}", 10)
	target := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	if err := d.UpdateMessageTimestamp(ctx, id, target); err != nil {
		t.Fatalf("UpdateMessageTimestamp: %v", err)
	}

	m, _ := d.GetMessage(ctx, id)
	if m.CreatedAt.Year() != 2025 || m.CreatedAt.Month() != 6 || m.CreatedAt.Day() != 15 {
		t.Errorf("expected 2025-06-15, got %v", m.CreatedAt)
	}
}

func TestCreateSummary_AndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	now := time.Now().UTC().Truncate(time.Second)
	sum := Summary{
		ID:           "sum-1",
		SessionID:    "s1",
		Kind:         "leaf",
		Depth:        0,
		Content:      "summary content",
		Tokens:       50,
		EarliestAt:   now.Add(-time.Hour),
		LatestAt:     now,
		SourceTokens: 300,
		Model:        "test-model",
	}
	if err := d.CreateSummary(ctx, sum); err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	got, err := d.GetSummary(ctx, "sum-1")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if got.Kind != "leaf" || got.Depth != 0 || got.Content != "summary content" {
		t.Errorf("unexpected summary: %+v", got)
	}
	if got.Model != "test-model" {
		t.Errorf("expected model=test-model, got %q", got.Model)
	}
	if got.SourceTokens != 300 {
		t.Errorf("expected source_tokens=300, got %d", got.SourceTokens)
	}
}

func TestGetSummary_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.GetSummary(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestLinkSummaryParents_AndGetChildren(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	now := time.Now().UTC().Truncate(time.Second)
	// Create two leaf summaries.
	for _, id := range []string{"child-1", "child-2"} {
		d.CreateSummary(ctx, Summary{
			ID: id, SessionID: "s1", Kind: "leaf", Depth: 0,
			Content: "leaf " + id, Tokens: 30,
			EarliestAt: now, LatestAt: now, SourceTokens: 100,
		})
	}

	// Create a condensed parent summary.
	d.CreateSummary(ctx, Summary{
		ID: "parent-1", SessionID: "s1", Kind: "condensed", Depth: 1,
		Content: "condensed summary", Tokens: 40,
		EarliestAt: now, LatestAt: now, SourceTokens: 60,
	})

	if err := d.LinkSummaryParents(ctx, "parent-1", []string{"child-1", "child-2"}); err != nil {
		t.Fatalf("LinkSummaryParents: %v", err)
	}

	children, err := d.GetSummaryChildren(ctx, "parent-1")
	if err != nil {
		t.Fatalf("GetSummaryChildren: %v", err)
	}
	if len(children) != 2 || children[0] != "child-1" || children[1] != "child-2" {
		t.Errorf("expected [child-1, child-2], got %v", children)
	}
}

func TestGetSummaryChildren_Empty(t *testing.T) {
	d := openTestDB(t)
	children, err := d.GetSummaryChildren(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetSummaryChildren: %v", err)
	}
	if children != nil {
		t.Errorf("expected nil for no children, got %v", children)
	}
}

func TestGetSummarySourceMessages_Empty(t *testing.T) {
	d := openTestDB(t)
	ids, err := d.GetSummarySourceMessages(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetSummarySourceMessages: %v", err)
	}
	if ids != nil {
		t.Errorf("expected nil for no source messages, got %v", ids)
	}
}

func TestReplaceContextItems(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Add 4 messages.
	for range 4 {
		d.AppendMessage(ctx, "s1", "user", "msg", "{}", 100)
	}

	items, _ := d.GetContextItems(ctx, "s1")
	if len(items) != 4 {
		t.Fatalf("expected 4 context items, got %d", len(items))
	}

	now := time.Now().UTC()
	d.CreateSummary(ctx, Summary{
		ID: "s-replace", SessionID: "s1", Kind: "leaf", Depth: 0,
		Content: "summary", Tokens: 50,
		EarliestAt: now, LatestAt: now, SourceTokens: 200,
	})

	// Replace first 2 items with a summary.
	if err := d.ReplaceContextItems(ctx, "s1", items[0].Ordinal, items[1].Ordinal, "s-replace"); err != nil {
		t.Fatalf("ReplaceContextItems: %v", err)
	}

	after, _ := d.GetContextItems(ctx, "s1")
	if len(after) != 3 {
		t.Fatalf("expected 3 context items after replace, got %d", len(after))
	}
	if after[0].ItemType != "summary" {
		t.Errorf("first item should be summary, got %q", after[0].ItemType)
	}
	if after[1].ItemType != "message" || after[2].ItemType != "message" {
		t.Error("remaining items should be messages")
	}
}

func TestMessageTokensOutsideTail_AllInTail(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.AppendMessage(ctx, "s1", "user", "msg1", "{}", 100)
	d.AppendMessage(ctx, "s1", "user", "msg2", "{}", 100)

	// Tail size >= total messages — nothing outside tail.
	outside, err := d.MessageTokensOutsideTail(ctx, "s1", 5)
	if err != nil {
		t.Fatalf("MessageTokensOutsideTail: %v", err)
	}
	if outside != 0 {
		t.Errorf("expected 0 tokens outside tail, got %d", outside)
	}
}

func TestOldestMessageContextItems_TokenLimit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Add 5 messages of 100 tokens each.
	for range 5 {
		d.AppendMessage(ctx, "s1", "user", "msg", "{}", 100)
	}

	// Tail=2, maxTokens=150 — should get at most 2 messages (100+100 > 150,
	// but the first message is taken regardless, second pushes past limit).
	items, msgs, err := d.OldestMessageContextItems(ctx, "s1", 2, 150)
	if err != nil {
		t.Fatalf("OldestMessageContextItems: %v", err)
	}
	// First msg (100) is taken, second (100+100=200 > 150) breaks.
	if len(items) != 1 || len(msgs) != 1 {
		t.Errorf("expected 1 item with 150 token limit, got %d items %d msgs", len(items), len(msgs))
	}
}

func TestMaxSummaryDepth_NoSummaries(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	depth, err := d.MaxSummaryDepth(ctx, "s1")
	if err != nil {
		t.Fatalf("MaxSummaryDepth: %v", err)
	}
	if depth != -1 {
		t.Errorf("expected -1 for no summaries, got %d", depth)
	}
}

func TestContiguousSummariesAtDepth(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	now := time.Now().UTC()

	// Add 3 messages, compact first two into summaries, keep third as message.
	d.AppendMessage(ctx, "s1", "user", "msg1", "{}", 100)
	d.AppendMessage(ctx, "s1", "user", "msg2", "{}", 100)
	d.AppendMessage(ctx, "s1", "user", "msg3", "{}", 100)

	items, _ := d.GetContextItems(ctx, "s1")

	// Replace item 0 with summary at depth 0.
	d.CreateSummary(ctx, Summary{
		ID: "sum-a", SessionID: "s1", Kind: "leaf", Depth: 0,
		Content: "a", Tokens: 30, EarliestAt: now, LatestAt: now, SourceTokens: 100,
	})
	d.ReplaceContextItems(ctx, "s1", items[0].Ordinal, items[0].Ordinal, "sum-a")

	// Replace item 1 with summary at depth 0.
	d.CreateSummary(ctx, Summary{
		ID: "sum-b", SessionID: "s1", Kind: "leaf", Depth: 0,
		Content: "b", Tokens: 30, EarliestAt: now, LatestAt: now, SourceTokens: 100,
	})
	d.ReplaceContextItems(ctx, "s1", items[1].Ordinal, items[1].Ordinal, "sum-b")

	// Should find 2 contiguous summaries at depth 0.
	ci, sums, err := d.ContiguousSummariesAtDepth(ctx, "s1", 0, 2)
	if err != nil {
		t.Fatalf("ContiguousSummariesAtDepth: %v", err)
	}
	if len(ci) != 2 || len(sums) != 2 {
		t.Errorf("expected 2 contiguous summaries, got %d items %d sums", len(ci), len(sums))
	}
}

func TestContiguousSummariesAtDepth_BelowMinCount(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	now := time.Now().UTC()
	d.AppendMessage(ctx, "s1", "user", "msg1", "{}", 100)
	items, _ := d.GetContextItems(ctx, "s1")

	d.CreateSummary(ctx, Summary{
		ID: "sum-only", SessionID: "s1", Kind: "leaf", Depth: 0,
		Content: "only", Tokens: 30, EarliestAt: now, LatestAt: now, SourceTokens: 100,
	})
	d.ReplaceContextItems(ctx, "s1", items[0].Ordinal, items[0].Ordinal, "sum-only")

	// minCount=3 but only 1 summary exists — should return nil.
	ci, sums, err := d.ContiguousSummariesAtDepth(ctx, "s1", 0, 3)
	if err != nil {
		t.Fatalf("ContiguousSummariesAtDepth: %v", err)
	}
	if ci != nil || sums != nil {
		t.Errorf("expected nil when below minCount, got %d items", len(ci))
	}
}

func TestSearchMessages_QueryTooLong(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	longQuery := strings.Repeat("x", 600)
	_, err := d.SearchMessages(ctx, "s1", longQuery, 10)
	if err == nil {
		t.Error("expected error for query too long")
	}
}

func TestSearchSummaries_QueryTooLong(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	longQuery := strings.Repeat("x", 600)
	_, err := d.SearchSummaries(ctx, "s1", longQuery, 10)
	if err == nil {
		t.Error("expected error for query too long")
	}
}

func TestSearchSummaries_Basic(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	now := time.Now().UTC()
	d.CreateSummary(ctx, Summary{
		ID: "sum-search", SessionID: "s1", Kind: "leaf", Depth: 0,
		Content: "the quick brown fox jumps over the lazy dog", Tokens: 50,
		EarliestAt: now, LatestAt: now, SourceTokens: 200,
	})

	results, err := d.SearchSummaries(ctx, "s1", "brown fox", 10)
	if err != nil {
		t.Fatalf("SearchSummaries: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if len(results) > 0 && results[0].Type != "summary" {
		t.Errorf("expected type=summary, got %q", results[0].Type)
	}
}

func TestSearch_Combined(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.AppendMessage(ctx, "s1", "user", "the quick brown fox", "{}", 10)

	now := time.Now().UTC()
	d.CreateSummary(ctx, Summary{
		ID: "sum-combined", SessionID: "s1", Kind: "leaf", Depth: 0,
		Content: "the quick brown fox summary", Tokens: 50,
		EarliestAt: now, LatestAt: now, SourceTokens: 100,
	})

	results, err := d.Search(ctx, "s1", "brown fox", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (message + summary), got %d", len(results))
	}
}

func TestSearch_LimitApplied(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Create several messages matching "alpha".
	for range 5 {
		d.AppendMessage(ctx, "s1", "user", "alpha content here", "{}", 10)
	}

	results, err := d.Search(ctx, "s1", "alpha", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(results))
	}
}

func TestSearchMessages_DefaultLimit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.AppendMessage(ctx, "s1", "user", "findme content", "{}", 10)

	// limit=0 should default to 20.
	results, err := d.SearchMessages(ctx, "s1", "findme", 0)
	if err != nil {
		t.Fatalf("SearchMessages default limit: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestContextTokenCount_WithSummary(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.AppendMessage(ctx, "s1", "user", "msg", "{}", 100)
	d.AppendMessage(ctx, "s1", "user", "msg", "{}", 200)

	items, _ := d.GetContextItems(ctx, "s1")

	now := time.Now().UTC()
	d.CreateSummary(ctx, Summary{
		ID: "sum-tok", SessionID: "s1", Kind: "leaf", Depth: 0,
		Content: "summary", Tokens: 50,
		EarliestAt: now, LatestAt: now, SourceTokens: 100,
	})
	d.ReplaceContextItems(ctx, "s1", items[0].Ordinal, items[0].Ordinal, "sum-tok")

	total, err := d.ContextTokenCount(ctx, "s1")
	if err != nil {
		t.Fatalf("ContextTokenCount: %v", err)
	}
	// 50 (summary) + 200 (remaining message)
	if total != 250 {
		t.Errorf("expected 250 tokens, got %d", total)
	}
}
