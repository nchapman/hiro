package db

import (
	"context"
	"testing"
	"time"
)

func TestInsertLogs_AndQueryLogs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	entries := []LogEntry{
		{Level: "INFO", Message: "server started", Component: "api", CreatedAt: time.Now().UTC()},
		{Level: "WARN", Message: "high latency", Component: "inference", InstanceID: "inst-1", CreatedAt: time.Now().UTC()},
		{Level: "ERROR", Message: "connection failed", Component: "cluster", Attrs: map[string]any{"host": "node-2"}, CreatedAt: time.Now().UTC()},
	}
	if err := d.InsertLogs(ctx, entries); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	// Query all — should return newest first.
	logs, err := d.QueryLogs(ctx, LogQuery{})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("got %d logs, want 3", len(logs))
	}
	// Newest first (highest ID first).
	if logs[0].Message != "connection failed" {
		t.Errorf("first log = %q, want 'connection failed'", logs[0].Message)
	}

	// Verify attrs round-trip.
	if logs[0].Attrs == nil || logs[0].Attrs["host"] != "node-2" {
		t.Errorf("attrs = %v, want {host: node-2}", logs[0].Attrs)
	}

	// Verify nullable fields.
	if logs[0].Component != "cluster" {
		t.Errorf("component = %q, want 'cluster'", logs[0].Component)
	}
	if logs[1].InstanceID != "inst-1" {
		t.Errorf("instance_id = %q, want 'inst-1'", logs[1].InstanceID)
	}
}

func TestQueryLogs_FilterByLevel(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	d.InsertLogs(ctx, []LogEntry{
		{Level: "INFO", Message: "a", CreatedAt: now},
		{Level: "WARN", Message: "b", CreatedAt: now},
		{Level: "ERROR", Message: "c", CreatedAt: now},
	})

	logs, _ := d.QueryLogs(ctx, LogQuery{Level: "WARN"})
	if len(logs) != 1 || logs[0].Message != "b" {
		t.Errorf("level filter: got %d logs, want 1 with message 'b'", len(logs))
	}
}

func TestQueryLogs_FilterByComponent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	d.InsertLogs(ctx, []LogEntry{
		{Level: "INFO", Message: "a", Component: "api", CreatedAt: now},
		{Level: "INFO", Message: "b", Component: "inference", CreatedAt: now},
	})

	logs, _ := d.QueryLogs(ctx, LogQuery{Component: "api"})
	if len(logs) != 1 || logs[0].Message != "a" {
		t.Errorf("component filter: got %d logs", len(logs))
	}
}

func TestQueryLogs_Search(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	d.InsertLogs(ctx, []LogEntry{
		{Level: "INFO", Message: "server started on port 8080", CreatedAt: now},
		{Level: "INFO", Message: "database opened", CreatedAt: now},
	})

	logs, _ := d.QueryLogs(ctx, LogQuery{Search: "port"})
	if len(logs) != 1 || logs[0].Message != "server started on port 8080" {
		t.Errorf("search filter: got %d logs", len(logs))
	}
}

func TestQueryLogs_SearchEscapesWildcards(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	d.InsertLogs(ctx, []LogEntry{
		{Level: "INFO", Message: "100% complete", CreatedAt: now},
		{Level: "INFO", Message: "50 items processed", CreatedAt: now},
	})

	// Search for literal "100%" — should not match everything.
	logs, _ := d.QueryLogs(ctx, LogQuery{Search: "100%"})
	if len(logs) != 1 {
		t.Errorf("wildcard escape: got %d logs, want 1", len(logs))
	}
}

func TestQueryLogs_CursorPagination(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		d.InsertLogs(ctx, []LogEntry{
			{Level: "INFO", Message: "msg", CreatedAt: now},
		})
	}

	// First page: limit 3.
	page1, _ := d.QueryLogs(ctx, LogQuery{Limit: 3})
	if len(page1) != 3 {
		t.Fatalf("page1: got %d, want 3", len(page1))
	}

	// Second page: before the oldest ID from page 1.
	oldestID := page1[len(page1)-1].ID
	page2, _ := d.QueryLogs(ctx, LogQuery{Limit: 3, Before: oldestID})
	if len(page2) != 2 {
		t.Fatalf("page2: got %d, want 2", len(page2))
	}

	// No overlap.
	if page2[0].ID >= oldestID {
		t.Error("page2 IDs should be less than cursor")
	}
}

func TestQueryLogs_LimitClamping(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	d.InsertLogs(ctx, []LogEntry{
		{Level: "INFO", Message: "a", CreatedAt: now},
		{Level: "INFO", Message: "b", CreatedAt: now},
	})

	// Default limit (0 → 200).
	logs, _ := d.QueryLogs(ctx, LogQuery{Limit: 0})
	if len(logs) != 2 {
		t.Errorf("default limit: got %d", len(logs))
	}

	// Explicit limit 1.
	logs, _ = d.QueryLogs(ctx, LogQuery{Limit: 1})
	if len(logs) != 1 {
		t.Errorf("limit 1: got %d", len(logs))
	}
}

func TestPruneLogs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-48 * time.Hour)
	recent := time.Now().UTC()
	d.InsertLogs(ctx, []LogEntry{
		{Level: "INFO", Message: "old", CreatedAt: old},
		{Level: "INFO", Message: "recent", CreatedAt: recent},
	})

	n, err := d.PruneLogs(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneLogs: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}

	logs, _ := d.QueryLogs(ctx, LogQuery{})
	if len(logs) != 1 || logs[0].Message != "recent" {
		t.Errorf("after prune: got %d logs", len(logs))
	}
}

func TestLogSources(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	d.InsertLogs(ctx, []LogEntry{
		{Level: "INFO", Message: "a", Component: "api", CreatedAt: now},
		{Level: "INFO", Message: "b", Component: "inference", CreatedAt: now},
		{Level: "INFO", Message: "c", Component: "api", CreatedAt: now},
		{Level: "INFO", Message: "d", CreatedAt: now}, // no component
	})

	sources, err := d.LogSources(ctx)
	if err != nil {
		t.Fatalf("LogSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(sources))
	}
	// Should be sorted.
	if sources[0] != "api" || sources[1] != "inference" {
		t.Errorf("sources = %v, want [api inference]", sources)
	}
}

func TestInsertLogs_Empty(t *testing.T) {
	d := openTestDB(t)
	if err := d.InsertLogs(context.Background(), nil); err != nil {
		t.Errorf("InsertLogs(nil) should be no-op, got: %v", err)
	}
}
