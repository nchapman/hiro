package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenStore_CreatesDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	s.Close()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("database file was not created")
	}
}

func TestOpenStore_MigrationsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	// Open twice — second open should not fail on migrations
	s1, err := OpenStore(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()

	s2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	s2.Close()
}

func TestAppendMessage(t *testing.T) {
	s := tempStore(t)

	id, err := s.AppendMessage("user", "hello world", `{"role":"user"}`, 3)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive ID, got %d", id)
	}

	// Verify the message is stored
	m, err := s.GetMessage(id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.Role != "user" || m.Content != "hello world" || m.Tokens != 3 {
		t.Errorf("got message %+v", m)
	}
	if m.Seq != 1 {
		t.Errorf("expected seq=1, got %d", m.Seq)
	}
}

func TestAppendMessage_SequentialSeq(t *testing.T) {
	s := tempStore(t)

	id1, _ := s.AppendMessage("user", "first", `{}`, 1)
	id2, _ := s.AppendMessage("assistant", "second", `{}`, 1)

	m1, _ := s.GetMessage(id1)
	m2, _ := s.GetMessage(id2)

	if m1.Seq != 1 || m2.Seq != 2 {
		t.Errorf("expected seq 1,2 got %d,%d", m1.Seq, m2.Seq)
	}
}

func TestAppendMessage_CreatesContextItem(t *testing.T) {
	s := tempStore(t)

	id1, _ := s.AppendMessage("user", "first", `{}`, 1)
	id2, _ := s.AppendMessage("assistant", "second", `{}`, 1)

	items, err := s.GetContextItems()
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 context items, got %d", len(items))
	}
	if items[0].ItemType != "message" || *items[0].MessageID != id1 {
		t.Errorf("item[0] = %+v", items[0])
	}
	if items[1].ItemType != "message" || *items[1].MessageID != id2 {
		t.Errorf("item[1] = %+v", items[1])
	}
	if items[0].Ordinal >= items[1].Ordinal {
		t.Errorf("ordinals not monotonically increasing: %d >= %d", items[0].Ordinal, items[1].Ordinal)
	}
}

func TestGetMessages_Batch(t *testing.T) {
	s := tempStore(t)

	id1, _ := s.AppendMessage("user", "first", `{}`, 1)
	s.AppendMessage("assistant", "skip", `{}`, 1)
	id3, _ := s.AppendMessage("user", "third", `{}`, 1)

	msgs, err := s.GetMessages([]int64{id3, id1}) // out of order
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	// Should be sorted by seq
	if msgs[0].Content != "first" || msgs[1].Content != "third" {
		t.Errorf("messages not in seq order: %q, %q", msgs[0].Content, msgs[1].Content)
	}
}

func TestCreateSummary_AndGet(t *testing.T) {
	s := tempStore(t)

	sum := Summary{
		ID:           "sum_test123",
		Kind:         "leaf",
		Depth:        0,
		Content:      "This is a summary of the conversation.",
		Tokens:       10,
		EarliestAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LatestAt:     time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC),
		SourceTokens: 100,
		Model:        "claude-sonnet-4-5-20250514",
	}
	if err := s.CreateSummary(sum); err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	got, err := s.GetSummary("sum_test123")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if got.Kind != "leaf" || got.Depth != 0 || got.Content != sum.Content {
		t.Errorf("got summary %+v", got)
	}
	if got.SourceTokens != 100 || got.Model != "claude-sonnet-4-5-20250514" {
		t.Errorf("metadata mismatch: %+v", got)
	}
}

func TestLinkSummaryMessages(t *testing.T) {
	s := tempStore(t)

	id1, _ := s.AppendMessage("user", "msg1", `{}`, 1)
	id2, _ := s.AppendMessage("assistant", "msg2", `{}`, 1)

	sum := Summary{
		ID: "sum_link1", Kind: "leaf", Depth: 0, Content: "summary",
		Tokens: 5, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 2,
	}
	s.CreateSummary(sum)
	if err := s.LinkSummaryMessages("sum_link1", []int64{id1, id2}); err != nil {
		t.Fatalf("LinkSummaryMessages: %v", err)
	}

	ids, err := s.GetSummarySourceMessages("sum_link1")
	if err != nil {
		t.Fatalf("GetSummarySourceMessages: %v", err)
	}
	if len(ids) != 2 || ids[0] != id1 || ids[1] != id2 {
		t.Errorf("expected [%d, %d], got %v", id1, id2, ids)
	}
}

func TestLinkSummaryParents(t *testing.T) {
	s := tempStore(t)

	for _, id := range []string{"sum_c1", "sum_c2", "sum_p1"} {
		s.CreateSummary(Summary{
			ID: id, Kind: "leaf", Depth: 0, Content: "x",
			Tokens: 1, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 1,
		})
	}

	if err := s.LinkSummaryParents("sum_p1", []string{"sum_c1", "sum_c2"}); err != nil {
		t.Fatalf("LinkSummaryParents: %v", err)
	}

	children, err := s.GetSummaryChildren("sum_p1")
	if err != nil {
		t.Fatalf("GetSummaryChildren: %v", err)
	}
	if len(children) != 2 || children[0] != "sum_c1" || children[1] != "sum_c2" {
		t.Errorf("expected [sum_c1, sum_c2], got %v", children)
	}
}

func TestReplaceContextItems(t *testing.T) {
	s := tempStore(t)

	// Add 5 messages
	for i := 0; i < 5; i++ {
		s.AppendMessage("user", "msg", `{}`, 10)
	}

	items, _ := s.GetContextItems()
	if len(items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(items))
	}

	// Create a summary and replace items 0-2 with it
	sum := Summary{
		ID: "sum_replace", Kind: "leaf", Depth: 0, Content: "summary of 0-2",
		Tokens: 5, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 30,
	}
	s.CreateSummary(sum)

	if err := s.ReplaceContextItems(items[0].Ordinal, items[2].Ordinal, "sum_replace"); err != nil {
		t.Fatalf("ReplaceContextItems: %v", err)
	}

	newItems, _ := s.GetContextItems()
	if len(newItems) != 3 { // 1 summary + 2 remaining messages
		t.Fatalf("expected 3 items after replacement, got %d", len(newItems))
	}
	if newItems[0].ItemType != "summary" || *newItems[0].SummaryID != "sum_replace" {
		t.Errorf("first item should be summary, got %+v", newItems[0])
	}
	if newItems[1].ItemType != "message" || newItems[2].ItemType != "message" {
		t.Error("remaining items should be messages")
	}
}

func TestContextTokenCount(t *testing.T) {
	s := tempStore(t)

	s.AppendMessage("user", "hello", `{}`, 10)
	s.AppendMessage("assistant", "world", `{}`, 20)

	total, err := s.ContextTokenCount()
	if err != nil {
		t.Fatalf("ContextTokenCount: %v", err)
	}
	if total != 30 {
		t.Errorf("expected 30 tokens, got %d", total)
	}
}

func TestContextTokenCount_WithSummaries(t *testing.T) {
	s := tempStore(t)

	s.AppendMessage("user", "hello", `{}`, 100)
	s.AppendMessage("assistant", "world", `{}`, 100)
	s.AppendMessage("user", "more", `{}`, 50)

	// Replace first two with a summary
	items, _ := s.GetContextItems()
	sum := Summary{
		ID: "sum_tc", Kind: "leaf", Depth: 0, Content: "summary",
		Tokens: 15, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 200,
	}
	s.CreateSummary(sum)
	s.ReplaceContextItems(items[0].Ordinal, items[1].Ordinal, "sum_tc")

	total, _ := s.ContextTokenCount()
	if total != 65 { // 15 (summary) + 50 (remaining message)
		t.Errorf("expected 65 tokens, got %d", total)
	}
}

func TestSearchMessages(t *testing.T) {
	s := tempStore(t)

	s.AppendMessage("user", "deploy the kubernetes cluster", `{}`, 5)
	s.AppendMessage("assistant", "I will help with the deployment", `{}`, 5)
	s.AppendMessage("user", "check the database status", `{}`, 5)

	results, err := s.SearchMessages("kubernetes", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'kubernetes'")
	}
	if results[0].Type != "message" {
		t.Errorf("expected type 'message', got %q", results[0].Type)
	}
}

func TestSearchSummaries(t *testing.T) {
	s := tempStore(t)

	s.CreateSummary(Summary{
		ID: "sum_s1", Kind: "leaf", Depth: 0,
		Content: "discussed kubernetes deployment strategies",
		Tokens:  5, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 50,
	})
	s.CreateSummary(Summary{
		ID: "sum_s2", Kind: "leaf", Depth: 0,
		Content: "reviewed database migration plan",
		Tokens:  5, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 50,
	})

	results, err := s.SearchSummaries("kubernetes", 10)
	if err != nil {
		t.Fatalf("SearchSummaries: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'kubernetes'")
	}
	if results[0].ID != "sum_s1" {
		t.Errorf("expected sum_s1, got %q", results[0].ID)
	}
}

func TestSearch_BothScopes(t *testing.T) {
	s := tempStore(t)

	s.AppendMessage("user", "fix the authentication bug", `{}`, 5)
	s.CreateSummary(Summary{
		ID: "sum_both", Kind: "leaf", Depth: 0,
		Content: "worked on authentication improvements",
		Tokens:  5, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 50,
	})

	results, err := s.Search("authentication", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	types := map[string]bool{}
	for _, r := range results {
		types[r.Type] = true
	}
	if !types["message"] || !types["summary"] {
		t.Error("expected results from both messages and summaries")
	}
}

func TestMessageTokensOutsideTail(t *testing.T) {
	s := tempStore(t)

	// Add 10 messages, each 100 tokens
	for i := 0; i < 10; i++ {
		s.AppendMessage("user", "message", `{}`, 100)
	}

	// With tail of 3, should have 7*100=700 tokens outside
	tokens, err := s.MessageTokensOutsideTail(3)
	if err != nil {
		t.Fatalf("MessageTokensOutsideTail: %v", err)
	}
	if tokens != 700 {
		t.Errorf("expected 700, got %d", tokens)
	}

	// With tail of 10, nothing outside
	tokens, _ = s.MessageTokensOutsideTail(10)
	if tokens != 0 {
		t.Errorf("expected 0, got %d", tokens)
	}
}

func TestOldestMessageContextItems(t *testing.T) {
	s := tempStore(t)

	// Add 10 messages at 100 tokens each
	for i := 0; i < 10; i++ {
		s.AppendMessage("user", "message", `{}`, 100)
	}

	// Get oldest messages outside tail of 3, up to 250 tokens
	items, msgs, err := s.OldestMessageContextItems(3, 250)
	if err != nil {
		t.Fatalf("OldestMessageContextItems: %v", err)
	}
	// 250 token budget → should get 2 messages (200 tokens), 3rd would be 300
	if len(items) != 2 || len(msgs) != 2 {
		t.Errorf("expected 2 items, got %d items and %d msgs", len(items), len(msgs))
	}
}

func TestContiguousSummariesAtDepth(t *testing.T) {
	s := tempStore(t)

	// Add some messages then replace with summaries at various depths
	for i := 0; i < 6; i++ {
		s.AppendMessage("user", "msg", `{}`, 10)
	}
	items, _ := s.GetContextItems()

	// Replace first 3 with depth-0 summaries
	for i := 0; i < 3; i++ {
		id := "sum_d0_" + string(rune('a'+i))
		s.CreateSummary(Summary{
			ID: id, Kind: "leaf", Depth: 0, Content: "summary",
			Tokens: 5, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 10,
		})
		s.ReplaceContextItems(items[i].Ordinal, items[i].Ordinal, id)
	}

	// Should find 3 contiguous depth-0 summaries
	citems, sums, err := s.ContiguousSummariesAtDepth(0, 2)
	if err != nil {
		t.Fatalf("ContiguousSummariesAtDepth: %v", err)
	}
	if len(citems) != 3 || len(sums) != 3 {
		t.Errorf("expected 3, got %d items and %d summaries", len(citems), len(sums))
	}

	// Min count too high — should return nil
	citems, sums, err = s.ContiguousSummariesAtDepth(0, 5)
	if err != nil {
		t.Fatalf("ContiguousSummariesAtDepth: %v", err)
	}
	if citems != nil || sums != nil {
		t.Error("expected nil when minCount > available")
	}
}

func TestMaxSummaryDepth(t *testing.T) {
	s := tempStore(t)

	// No summaries
	d, _ := s.MaxSummaryDepth()
	if d != -1 {
		t.Errorf("expected -1, got %d", d)
	}

	// Add messages and replace some with summaries
	s.AppendMessage("user", "msg", `{}`, 10)
	items, _ := s.GetContextItems()

	s.CreateSummary(Summary{
		ID: "sum_md", Kind: "condensed", Depth: 2, Content: "deep summary",
		Tokens: 5, EarliestAt: time.Now(), LatestAt: time.Now(), SourceTokens: 100,
	})
	s.ReplaceContextItems(items[0].Ordinal, items[0].Ordinal, "sum_md")

	d, _ = s.MaxSummaryDepth()
	if d != 2 {
		t.Errorf("expected 2, got %d", d)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"hi", 1},                         // 2 chars → 1 (minimum for non-empty)
		{"hello", 1},                      // 5 chars → 1
		{"hello world this is a test", 6}, // 26 chars → 6
	}
	for _, tc := range tests {
		got := EstimateTokens(tc.input)
		if got != tc.expected {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.input, got, tc.expected)
		}
	}
}
