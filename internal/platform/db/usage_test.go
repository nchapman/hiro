package db

import (
	"context"
	"testing"
)

func TestRecordTurnUsage_AutoIncrementsTurn(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Record 3 separate turns.
	for i := 0; i < 3; i++ {
		d.RecordTurnUsage(ctx, []UsageEvent{
			{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 100, Cost: 0.01},
		})
	}

	last, ok, err := d.GetLastUsageEvent(ctx, "s1")
	if err != nil {
		t.Fatalf("GetLastUsageEvent: %v", err)
	}
	if !ok {
		t.Fatal("expected last usage event")
	}
	if last.Turn != 3 {
		t.Errorf("expected turn=3, got %d", last.Turn)
	}
}

func TestGetLastUsageEvent_NoEvents(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	_, ok, err := d.GetLastUsageEvent(ctx, "s1")
	if err != nil {
		t.Fatalf("GetLastUsageEvent: %v", err)
	}
	if ok {
		t.Error("expected no last event for empty session")
	}
}

func TestGetLastTurnUsage_NoEvents(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	_, ok, err := d.GetLastTurnUsage(ctx, "s1")
	if err != nil {
		t.Fatalf("GetLastTurnUsage: %v", err)
	}
	if ok {
		t.Error("expected no last turn for empty session")
	}
}

func TestGetLastTurnUsage_MultipleSteps(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	// Turn with 3 steps.
	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 100, OutputTokens: 50, ReasoningTokens: 10, Cost: 0.01},
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 200, OutputTokens: 100, ReasoningTokens: 20, Cost: 0.02},
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 300, OutputTokens: 150, ReasoningTokens: 30, Cost: 0.03},
	})

	turn, ok, err := d.GetLastTurnUsage(ctx, "s1")
	if err != nil {
		t.Fatalf("GetLastTurnUsage: %v", err)
	}
	if !ok {
		t.Fatal("expected turn data")
	}
	if turn.TotalInputTokens != 600 {
		t.Errorf("expected input=600, got %d", turn.TotalInputTokens)
	}
	if turn.TotalOutputTokens != 300 {
		t.Errorf("expected output=300, got %d", turn.TotalOutputTokens)
	}
	if turn.TotalReasoningTokens != 60 {
		t.Errorf("expected reasoning=60, got %d", turn.TotalReasoningTokens)
	}
	if turn.EventCount != 3 {
		t.Errorf("expected 3 events, got %d", turn.EventCount)
	}
}

func TestGetUsageByModel_MultipleModels(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s1", Model: "claude-3-opus", Provider: "anthropic", InputTokens: 1000, Cost: 0.05},
	})
	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s1", Model: "gpt-4", Provider: "openai", InputTokens: 2000, Cost: 0.10},
	})
	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s1", Model: "claude-3-opus", Provider: "anthropic", InputTokens: 500, Cost: 0.025},
	})

	byModel, err := d.GetUsageByModel(ctx)
	if err != nil {
		t.Fatalf("GetUsageByModel: %v", err)
	}
	if len(byModel) != 2 {
		t.Fatalf("expected 2 models, got %d", len(byModel))
	}

	// Sorted by cost DESC, so openai/gpt-4 first (0.10 > 0.075).
	if byModel[0].Model != "gpt-4" || byModel[0].Provider != "openai" {
		t.Errorf("expected gpt-4/openai first, got %s/%s", byModel[0].Model, byModel[0].Provider)
	}
	if byModel[0].TotalInputTokens != 2000 {
		t.Errorf("expected gpt-4 input=2000, got %d", byModel[0].TotalInputTokens)
	}
	if byModel[1].Model != "claude-3-opus" || byModel[1].TotalInputTokens != 1500 {
		t.Errorf("expected claude-3-opus input=1500, got %s/%d", byModel[1].Model, byModel[1].TotalInputTokens)
	}
}

func TestGetUsageByDay_DefaultLimit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 100, Cost: 0.01},
	})

	// limit=0 defaults to 30.
	byDay, err := d.GetUsageByDay(ctx, 0)
	if err != nil {
		t.Fatalf("GetUsageByDay: %v", err)
	}
	if len(byDay) != 1 {
		t.Errorf("expected 1 day, got %d", len(byDay))
	}
}

func TestGetUsageByDay_NegativeLimit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 100, Cost: 0.01},
	})

	// Negative limit should default to 30.
	byDay, err := d.GetUsageByDay(ctx, -5)
	if err != nil {
		t.Fatalf("GetUsageByDay: %v", err)
	}
	if len(byDay) != 1 {
		t.Errorf("expected 1 day, got %d", len(byDay))
	}
}

func TestGetSessionUsage_EmptySession(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	usage, err := d.GetSessionUsage(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSessionUsage: %v", err)
	}
	if usage.EventCount != 0 || usage.TotalInputTokens != 0 || usage.TotalCost != 0 {
		t.Errorf("expected zero usage for empty session, got %+v", usage)
	}
}

func TestGetTotalUsage_Empty(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	total, err := d.GetTotalUsage(ctx)
	if err != nil {
		t.Fatalf("GetTotalUsage: %v", err)
	}
	if total.EventCount != 0 {
		t.Errorf("expected 0 events, got %d", total.EventCount)
	}
}

func TestGetTotalUsage_MultipleSessions(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})
	d.CreateSession(ctx, Session{ID: "s2", AgentName: "test", Mode: "persistent"})

	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s1", Model: "m", Provider: "p", InputTokens: 100, Cost: 0.01},
	})
	d.RecordTurnUsage(ctx, []UsageEvent{
		{SessionID: "s2", Model: "m", Provider: "p", InputTokens: 200, Cost: 0.02},
	})

	total, err := d.GetTotalUsage(ctx)
	if err != nil {
		t.Fatalf("GetTotalUsage: %v", err)
	}
	if total.TotalInputTokens != 300 {
		t.Errorf("expected total input=300, got %d", total.TotalInputTokens)
	}
	if total.EventCount != 2 {
		t.Errorf("expected 2 events, got %d", total.EventCount)
	}
}

func TestUsage_AllTokenFields(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	d.CreateSession(ctx, Session{ID: "s1", AgentName: "test", Mode: "persistent"})

	d.RecordTurnUsage(ctx, []UsageEvent{
		{
			SessionID:        "s1",
			Model:            "m",
			Provider:         "p",
			InputTokens:      1000,
			OutputTokens:     500,
			ReasoningTokens:  100,
			CacheReadTokens:  200,
			CacheWriteTokens: 50,
			Cost:             0.05,
		},
	})

	usage, err := d.GetSessionUsage(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSessionUsage: %v", err)
	}
	if usage.TotalInputTokens != 1000 {
		t.Errorf("input: got %d, want 1000", usage.TotalInputTokens)
	}
	if usage.TotalOutputTokens != 500 {
		t.Errorf("output: got %d, want 500", usage.TotalOutputTokens)
	}
	if usage.TotalReasoningTokens != 100 {
		t.Errorf("reasoning: got %d, want 100", usage.TotalReasoningTokens)
	}
	if usage.TotalCacheReadTokens != 200 {
		t.Errorf("cache read: got %d, want 200", usage.TotalCacheReadTokens)
	}
	if usage.TotalCacheWriteTokens != 50 {
		t.Errorf("cache write: got %d, want 50", usage.TotalCacheWriteTokens)
	}
}
