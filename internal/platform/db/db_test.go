package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpen_CreatesDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hiro.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d.Close()

	// Re-open to verify migrations are idempotent.
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	d2.Close()
}

func TestSessions_CRUD(t *testing.T) {
	d := openTestDB(t)

	// Create.
	err := d.CreateSession(Session{
		ID:        "sess-1",
		AgentName: "coordinator",
		Mode:      "persistent",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Create child.
	err = d.CreateSession(Session{
		ID:        "sess-2",
		AgentName: "researcher",
		Mode:      "ephemeral",
		ParentID:  "sess-1",
	})
	if err != nil {
		t.Fatalf("CreateSession child: %v", err)
	}

	// Get.
	s, err := d.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.AgentName != "coordinator" || s.Mode != "persistent" || s.Status != "running" {
		t.Errorf("unexpected session: %+v", s)
	}

	// Get child with parent.
	s2, err := d.GetSession("sess-2")
	if err != nil {
		t.Fatalf("GetSession child: %v", err)
	}
	if s2.ParentID != "sess-1" {
		t.Errorf("expected parent_id sess-1, got %q", s2.ParentID)
	}

	// List children.
	children, err := d.ListChildSessions("sess-1")
	if err != nil {
		t.Fatalf("ListChildSessions: %v", err)
	}
	if len(children) != 1 || children[0].ID != "sess-2" {
		t.Errorf("expected 1 child, got %d", len(children))
	}

	// Update status.
	if err := d.UpdateSessionStatus("sess-1", "stopped"); err != nil {
		t.Fatalf("UpdateSessionStatus: %v", err)
	}
	s, _ = d.GetSession("sess-1")
	if s.Status != "stopped" || s.StoppedAt == nil {
		t.Errorf("expected stopped with timestamp, got %+v", s)
	}

	// Instance descendant checking.
	if err := d.CreateInstance(Instance{ID: "inst-1", AgentName: "coordinator", Mode: "persistent"}); err != nil {
		t.Fatalf("CreateInstance parent: %v", err)
	}
	if err := d.CreateInstance(Instance{ID: "inst-2", AgentName: "researcher", Mode: "persistent", ParentID: "inst-1"}); err != nil {
		t.Fatalf("CreateInstance child: %v", err)
	}
	ok, err := d.IsInstanceDescendant("inst-2", "inst-1")
	if err != nil {
		t.Fatalf("IsInstanceDescendant: %v", err)
	}
	if !ok {
		t.Error("expected inst-2 to be descendant of inst-1")
	}
	ok, _ = d.IsInstanceDescendant("inst-1", "inst-2")
	if ok {
		t.Error("inst-1 should not be descendant of inst-2")
	}

	// Delete parent — child is cascade-deleted.
	if err := d.DeleteSession("sess-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	_, err = d.GetSession("sess-2")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected child to be cascade-deleted, got: %v", err)
	}
}

func TestLatestSessionByInstance(t *testing.T) {
	d := openTestDB(t)

	// Create an instance.
	d.CreateInstance(Instance{ID: "inst-A", AgentName: "test", Mode: "persistent"})

	// No sessions yet.
	_, ok, err := d.LatestSessionByInstance("inst-A")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected no session for fresh instance")
	}

	// Create two sessions with different timestamps.
	d.db.Exec(`INSERT INTO sessions (id, instance_id, agent_name, mode, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"sess-old", "inst-A", "test", "persistent", "running", "2026-01-01 00:00:00")
	d.db.Exec(`INSERT INTO sessions (id, instance_id, agent_name, mode, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"sess-new", "inst-A", "test", "persistent", "running", "2026-01-02 00:00:00")

	// Should return the newest one.
	sess, ok, err := d.LatestSessionByInstance("inst-A")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected to find a session")
	}
	if sess.ID != "sess-new" {
		t.Errorf("expected sess-new, got %s", sess.ID)
	}
	if sess.InstanceID != "inst-A" {
		t.Errorf("expected instance_id inst-A, got %s", sess.InstanceID)
	}
}

func TestMessages_AppendAndRetrieve(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Append messages.
	id1, err := d.AppendMessage("s1", "user", "hello", `{"role":"user"}`, 10)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	id2, err := d.AppendMessage("s1", "assistant", "hi there", `{"role":"assistant"}`, 15)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// GetMessage.
	m, err := d.GetMessage(id1)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.Role != "user" || m.Content != "hello" || m.SessionID != "s1" || m.Seq != 1 {
		t.Errorf("unexpected message: %+v", m)
	}

	// GetMessages.
	msgs, err := d.GetMessages([]int64{id2, id1})
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Seq != 1 || msgs[1].Seq != 2 {
		t.Errorf("expected 2 messages in seq order, got %+v", msgs)
	}

	// RecentMessages.
	recent, err := d.RecentMessages("s1", 1)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(recent) != 1 || recent[0].Content != "hi there" {
		t.Errorf("expected most recent message, got %+v", recent)
	}

	// Context items created.
	items, err := d.GetContextItems("s1")
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	if len(items) != 2 || items[0].ItemType != "message" || items[1].ItemType != "message" {
		t.Errorf("expected 2 message context items, got %+v", items)
	}

	// ContextTokenCount.
	total, err := d.ContextTokenCount("s1")
	if err != nil {
		t.Fatalf("ContextTokenCount: %v", err)
	}
	if total != 25 {
		t.Errorf("expected 25 tokens, got %d", total)
	}
}

func TestMessages_SessionScoping(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "a", Mode: "persistent"})
	d.CreateSession(Session{ID: "s2", AgentName: "b", Mode: "persistent"})

	d.AppendMessage("s1", "user", "session one", "{}", 10)
	d.AppendMessage("s2", "user", "session two", "{}", 10)

	// Each session sees only its own messages.
	msgs1, _ := d.RecentMessages("s1", 10)
	msgs2, _ := d.RecentMessages("s2", 10)
	if len(msgs1) != 1 || msgs1[0].Content != "session one" {
		t.Errorf("s1 got wrong messages: %+v", msgs1)
	}
	if len(msgs2) != 1 || msgs2[0].Content != "session two" {
		t.Errorf("s2 got wrong messages: %+v", msgs2)
	}

	// Sequence numbers are independent per session.
	if msgs1[0].Seq != 1 || msgs2[0].Seq != 1 {
		t.Errorf("expected independent seq=1 for each session")
	}
}

func TestCompaction_Workflow(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Add messages.
	for i := 0; i < 5; i++ {
		d.AppendMessage("s1", "user", "msg", "{}", 100)
	}

	// Verify tokens outside tail.
	outside, err := d.MessageTokensOutsideTail("s1", 2)
	if err != nil {
		t.Fatalf("MessageTokensOutsideTail: %v", err)
	}
	if outside != 300 { // 5 messages - 2 tail = 3 * 100
		t.Errorf("expected 300 tokens outside tail, got %d", outside)
	}

	// Get oldest items for compaction.
	items, msgs, err := d.OldestMessageContextItems("s1", 2, 500)
	if err != nil {
		t.Fatalf("OldestMessageContextItems: %v", err)
	}
	if len(items) != 3 || len(msgs) != 3 {
		t.Errorf("expected 3 compactable items, got %d items %d msgs", len(items), len(msgs))
	}

	// Create summary and replace context items.
	sum := Summary{
		ID:           "sum_test1",
		SessionID:    "s1",
		Kind:         "leaf",
		Depth:        0,
		Content:      "summary of messages",
		Tokens:       50,
		EarliestAt:   time.Now(),
		LatestAt:     time.Now(),
		SourceTokens: 300,
	}
	if err := d.CreateSummary(sum); err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	msgIDs := make([]int64, len(msgs))
	for i, m := range msgs {
		msgIDs[i] = m.ID
	}
	if err := d.LinkSummaryMessages("sum_test1", msgIDs); err != nil {
		t.Fatalf("LinkSummaryMessages: %v", err)
	}
	if err := d.ReplaceContextItems("s1", items[0].Ordinal, items[len(items)-1].Ordinal, "sum_test1"); err != nil {
		t.Fatalf("ReplaceContextItems: %v", err)
	}

	// Verify context items: 1 summary + 2 tail messages.
	ciAfter, err := d.GetContextItems("s1")
	if err != nil {
		t.Fatalf("GetContextItems after compaction: %v", err)
	}
	if len(ciAfter) != 3 {
		t.Fatalf("expected 3 context items after compaction, got %d", len(ciAfter))
	}
	if ciAfter[0].ItemType != "summary" {
		t.Errorf("expected first item to be summary, got %q", ciAfter[0].ItemType)
	}

	// Token count should reflect summary + remaining messages.
	total, _ := d.ContextTokenCount("s1")
	if total != 250 { // 50 (summary) + 100*2 (tail)
		t.Errorf("expected 250 tokens after compaction, got %d", total)
	}

	// Max summary depth.
	depth, _ := d.MaxSummaryDepth("s1")
	if depth != 0 {
		t.Errorf("expected max depth 0, got %d", depth)
	}

	// GetSummary.
	retrieved, err := d.GetSummary("sum_test1")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if retrieved.Kind != "leaf" || retrieved.SourceTokens != 300 {
		t.Errorf("unexpected summary: %+v", retrieved)
	}

	// GetSummarySourceMessages.
	sourceIDs, err := d.GetSummarySourceMessages("sum_test1")
	if err != nil {
		t.Fatalf("GetSummarySourceMessages: %v", err)
	}
	if len(sourceIDs) != 3 {
		t.Errorf("expected 3 source messages, got %d", len(sourceIDs))
	}
}

func TestSearch_FTS(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})
	d.CreateSession(Session{ID: "s2", AgentName: "test", Mode: "persistent"})

	d.AppendMessage("s1", "user", "the quick brown fox", "{}", 10)
	d.AppendMessage("s1", "user", "lazy dog sleeps", "{}", 10)
	d.AppendMessage("s2", "user", "the quick brown fox jumps", "{}", 10)

	// Session-scoped search.
	results, err := d.SearchMessages("s1", "quick brown", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result in s1, got %d", len(results))
	}

}

func TestUsage_RecordAndAggregate(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Record usage events via RecordTurnUsage.
	if err := d.RecordTurnUsage([]UsageEvent{
		{
			SessionID:    "s1",
			Model:        "claude-sonnet-4-6",
			Provider:     "anthropic",
			InputTokens:  1000,
			OutputTokens: 500,
			Cost:         0.01,
		},
		{
			SessionID:       "s1",
			Model:           "claude-sonnet-4-6",
			Provider:        "anthropic",
			InputTokens:     2000,
			OutputTokens:    1000,
			CacheReadTokens: 500,
			Cost:            0.02,
		},
	}); err != nil {
		t.Fatalf("RecordTurnUsage: %v", err)
	}

	// Session usage.
	usage, err := d.GetSessionUsage("s1")
	if err != nil {
		t.Fatalf("GetSessionUsage: %v", err)
	}
	if usage.TotalInputTokens != 3000 || usage.TotalOutputTokens != 1500 || usage.TotalCacheReadTokens != 500 {
		t.Errorf("unexpected session usage: %+v", usage)
	}
	if usage.TotalCost != 0.03 {
		t.Errorf("expected cost 0.03, got %f", usage.TotalCost)
	}
	if usage.EventCount != 2 {
		t.Errorf("expected 2 events, got %d", usage.EventCount)
	}

	// Total usage.
	total, _ := d.GetTotalUsage()
	if total.TotalInputTokens != 3000 {
		t.Errorf("total usage mismatch: %+v", total)
	}

	// By model.
	byModel, err := d.GetUsageByModel()
	if err != nil {
		t.Fatalf("GetUsageByModel: %v", err)
	}
	if len(byModel) != 1 || byModel[0].Model != "claude-sonnet-4-6" {
		t.Errorf("unexpected by-model: %+v", byModel)
	}

	// By day.
	byDay, err := d.GetUsageByDay(7)
	if err != nil {
		t.Fatalf("GetUsageByDay: %v", err)
	}
	if len(byDay) != 1 {
		t.Errorf("expected 1 day, got %d", len(byDay))
	}
}

func TestUsage_TurnGrouping(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Turn 1: two steps (e.g., tool-use turn).
	err := d.RecordTurnUsage([]UsageEvent{
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 1000, OutputTokens: 200, Cost: 0.01},
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 1500, OutputTokens: 300, Cost: 0.02},
	})
	if err != nil {
		t.Fatalf("RecordTurnUsage turn 1: %v", err)
	}

	// Turn 2: single step.
	err = d.RecordTurnUsage([]UsageEvent{
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 2000, OutputTokens: 400, Cost: 0.03},
	})
	if err != nil {
		t.Fatalf("RecordTurnUsage turn 2: %v", err)
	}

	// GetLastTurnUsage should return only turn 2.
	turn, ok, err := d.GetLastTurnUsage("s1")
	if err != nil {
		t.Fatalf("GetLastTurnUsage: %v", err)
	}
	if !ok {
		t.Fatal("expected last turn usage, got none")
	}
	if turn.TotalInputTokens != 2000 || turn.TotalOutputTokens != 400 {
		t.Errorf("last turn: input=%d output=%d, want 2000/400", turn.TotalInputTokens, turn.TotalOutputTokens)
	}
	if turn.TotalCost != 0.03 {
		t.Errorf("last turn cost: %f, want 0.03", turn.TotalCost)
	}
	if turn.EventCount != 1 {
		t.Errorf("last turn events: %d, want 1", turn.EventCount)
	}

	// GetLastUsageEvent should return the last step of turn 2.
	last, ok, err := d.GetLastUsageEvent("s1")
	if err != nil {
		t.Fatalf("GetLastUsageEvent: %v", err)
	}
	if !ok {
		t.Fatal("expected last event, got none")
	}
	if last.InputTokens != 2000 || last.Turn != 2 {
		t.Errorf("last event: input=%d turn=%d, want 2000/2", last.InputTokens, last.Turn)
	}

	// Session totals should include all events from both turns.
	session, err := d.GetSessionUsage("s1")
	if err != nil {
		t.Fatalf("GetSessionUsage: %v", err)
	}
	if session.TotalInputTokens != 4500 { // 1000+1500+2000
		t.Errorf("session input: %d, want 4500", session.TotalInputTokens)
	}
	if session.EventCount != 3 {
		t.Errorf("session events: %d, want 3", session.EventCount)
	}
}

func TestUsage_TurnGrouping_LegacyTurn0(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Simulate a legacy turn-0 row (pre-migration data) via direct insert.
	if _, err := d.db.Exec(`INSERT INTO usage_events (session_id, model, provider, turn, input_tokens, cost) VALUES (?, ?, ?, 0, 9999, 0.99)`, "s1", "m", "p"); err != nil {
		t.Fatalf("inserting legacy turn-0 row: %v", err)
	}

	// GetLastTurnUsage should not return turn-0 data.
	_, ok, err := d.GetLastTurnUsage("s1")
	if err != nil {
		t.Fatalf("GetLastTurnUsage: %v", err)
	}
	if ok {
		t.Error("expected no last turn for turn-0 only session")
	}

	// GetLastUsageEvent should also skip turn-0.
	_, ok, err = d.GetLastUsageEvent("s1")
	if err != nil {
		t.Fatalf("GetLastUsageEvent: %v", err)
	}
	if ok {
		t.Error("expected no last event for turn-0 only session")
	}

	// But session totals should still include turn-0 rows.
	session, err := d.GetSessionUsage("s1")
	if err != nil {
		t.Fatalf("GetSessionUsage: %v", err)
	}
	if session.TotalInputTokens != 9999 {
		t.Errorf("session input: %d, want 9999", session.TotalInputTokens)
	}
}

func TestUsage_RecordTurnUsage_Empty(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Empty slice should be a no-op.
	if err := d.RecordTurnUsage(nil); err != nil {
		t.Errorf("RecordTurnUsage(nil): %v", err)
	}
	if err := d.RecordTurnUsage([]UsageEvent{}); err != nil {
		t.Errorf("RecordTurnUsage([]): %v", err)
	}

	session, _ := d.GetSessionUsage("s1")
	if session.EventCount != 0 {
		t.Errorf("expected 0 events, got %d", session.EventCount)
	}
}

func TestDeleteSession_Cascades(t *testing.T) {
	d := openTestDB(t)
	d.CreateSession(Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.AppendMessage("s1", "user", "hello", "{}", 10)
	d.RecordTurnUsage([]UsageEvent{{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 100}})
	if _, err := d.db.Exec(`INSERT INTO request_log (session_id, model) VALUES (?, ?)`, "s1", "m"); err != nil {
		t.Fatalf("inserting test request_log row: %v", err)
	}

	if err := d.DeleteSession("s1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// All related data should be gone.
	msgs, _ := d.RecentMessages("s1", 10)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after delete, got %d", len(msgs))
	}

	usage, _ := d.GetSessionUsage("s1")
	if usage.EventCount != 0 {
		t.Errorf("expected 0 usage events after delete, got %d", usage.EventCount)
	}

	var logCount int
	d.db.QueryRow(`SELECT COUNT(*) FROM request_log WHERE session_id = ?`, "s1").Scan(&logCount)
	if logCount != 0 {
		t.Errorf("expected 0 log entries after delete, got %d", logCount)
	}
}
