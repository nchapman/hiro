package inference

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

var testScheduleLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

func openTestPDB(t *testing.T) *platformdb.DB {
	t.Helper()
	dir := t.TempDir()
	pdb, err := platformdb.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Close() })

	// Create a parent instance for subscriptions.
	if err := pdb.CreateInstance(context.Background(), platformdb.Instance{
		ID: "test-inst", AgentName: "operator", Mode: "persistent",
	}); err != nil {
		t.Fatal(err)
	}
	return pdb
}

// fakeScheduleCallback records Add/Remove calls for verification.
type fakeScheduleCallback struct {
	added   []platformdb.Subscription
	removed []string
}

func (f *fakeScheduleCallback) Add(sub platformdb.Subscription) { f.added = append(f.added, sub) }
func (f *fakeScheduleCallback) Remove(id string)                { f.removed = append(f.removed, id) }

func runScheduleTool(t *testing.T, tool Tool, input string) (string, bool) {
	t.Helper()
	ctx := ContextWithCallerID(context.Background(), "test-inst")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: tool.Info().Name, Input: input})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp.Content, resp.IsError
}

// --- parseScheduleTime tests ---

func TestParseScheduleTime_Duration(t *testing.T) {
	before := time.Now()
	got, err := parseScheduleTime("20m", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if got.Before(before.Add(20*time.Minute)) || got.After(after.Add(20*time.Minute)) {
		t.Errorf("expected ~20m from now, got %v", got)
	}
}

func TestParseScheduleTime_RFC3339(t *testing.T) {
	got, err := parseScheduleTime("2026-12-25T09:00:00Z", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	expected := time.Date(2026, 12, 25, 9, 0, 0, 0, time.UTC)
	if !got.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, got)
	}
}

func TestParseScheduleTime_LocalTime(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	got, err := parseScheduleTime("2026-12-25T09:00:00", ny)
	if err != nil {
		t.Fatal(err)
	}
	expected := time.Date(2026, 12, 25, 9, 0, 0, 0, ny)
	if !got.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, got)
	}
}

func TestParseScheduleTime_Now(t *testing.T) {
	before := time.Now()
	got, err := parseScheduleTime("now", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// Should be ~1 second from now (not in the past).
	if !got.After(before) {
		t.Error("expected future time for 'now'")
	}
	if got.After(time.Now().Add(5 * time.Second)) {
		t.Error("expected near-future time for 'now'")
	}
}

func TestParseScheduleTime_NowCaseInsensitive(t *testing.T) {
	got, err := parseScheduleTime("NOW", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !got.After(time.Now()) {
		t.Error("expected future time for 'NOW'")
	}
}

func TestParseScheduleTime_ZeroDuration(t *testing.T) {
	before := time.Now()
	got, err := parseScheduleTime("0s", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !got.After(before) {
		t.Error("expected future time for '0s'")
	}
}

func TestParseScheduleTime_NegativeDuration(t *testing.T) {
	before := time.Now()
	got, err := parseScheduleTime("-5m", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// Negative durations are treated as immediate (same as "now").
	if !got.After(before) {
		t.Error("expected future time for negative duration")
	}
}

func TestParseScheduleTime_Invalid(t *testing.T) {
	_, err := parseScheduleTime("not-a-time", time.UTC)
	if err == nil {
		t.Error("expected error for invalid time")
	}
}

// --- ScheduleRecurring tool tests ---

func TestScheduleRecurring_Valid(t *testing.T) {
	pdb := openTestPDB(t)
	cb := &fakeScheduleCallback{}
	tools := buildScheduleTools(pdb, cb, time.UTC)

	content, isErr := runScheduleTool(t, tools[0], `{"name":"daily","schedule":"0 9 * * *","message":"do it"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "daily") {
		t.Errorf("expected name in response, got %q", content)
	}
	if len(cb.added) != 1 {
		t.Fatalf("expected 1 add callback, got %d", len(cb.added))
	}
	if cb.added[0].Name != "daily" || cb.added[0].Trigger.Expr != "0 9 * * *" {
		t.Errorf("unexpected subscription: %+v", cb.added[0])
	}
}

func TestScheduleRecurring_InvalidCron(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	content, isErr := runScheduleTool(t, tools[0], `{"name":"bad","schedule":"invalid","message":"x"}`)
	if !isErr {
		t.Fatal("expected error for invalid cron")
	}
	if !strings.Contains(content, "invalid cron") {
		t.Errorf("expected cron error message, got %q", content)
	}
}

func TestScheduleRecurring_DuplicateName(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	runScheduleTool(t, tools[0], `{"name":"dup","schedule":"* * * * *","message":"x"}`)
	content, isErr := runScheduleTool(t, tools[0], `{"name":"dup","schedule":"* * * * *","message":"y"}`)
	if !isErr {
		t.Fatal("expected error for duplicate name")
	}
	if !strings.Contains(content, "already exists") {
		t.Errorf("expected duplicate error, got %q", content)
	}
}

func TestScheduleRecurring_EmptyFields(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty name", `{"name":"","schedule":"* * * * *","message":"x"}`, "name is required"},
		{"empty schedule", `{"name":"x","schedule":"","message":"x"}`, "schedule is required"},
		{"empty message", `{"name":"x","schedule":"* * * * *","message":""}`, "message is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, isErr := runScheduleTool(t, tools[0], tt.input)
			if !isErr {
				t.Fatal("expected error")
			}
			if !strings.Contains(content, tt.want) {
				t.Errorf("expected %q, got %q", tt.want, content)
			}
		})
	}
}

// --- ScheduleOnce tool tests ---

func TestScheduleOnce_RelativeDuration(t *testing.T) {
	pdb := openTestPDB(t)
	cb := &fakeScheduleCallback{}
	tools := buildScheduleTools(pdb, cb, time.UTC)

	content, isErr := runScheduleTool(t, tools[1], `{"name":"reminder","at":"30m","message":"check in"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "reminder") {
		t.Errorf("expected name in response, got %q", content)
	}
	if len(cb.added) != 1 || cb.added[0].Trigger.Type != "once" {
		t.Fatalf("expected once trigger, got %+v", cb.added)
	}
}

func TestScheduleOnce_Now(t *testing.T) {
	pdb := openTestPDB(t)
	cb := &fakeScheduleCallback{}
	tools := buildScheduleTools(pdb, cb, time.UTC)

	content, isErr := runScheduleTool(t, tools[1], `{"name":"immediate","at":"now","message":"do it now"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "immediate") {
		t.Errorf("expected name in response, got %q", content)
	}
	if len(cb.added) != 1 || cb.added[0].Trigger.Type != "once" {
		t.Fatalf("expected once trigger, got %+v", cb.added)
	}
}

func TestScheduleOnce_AbsoluteTime(t *testing.T) {
	pdb := openTestPDB(t)
	cb := &fakeScheduleCallback{}
	tools := buildScheduleTools(pdb, cb, time.UTC)

	future := time.Now().Add(2 * time.Hour).UTC().Format("2006-01-02T15:04:05")
	content, isErr := runScheduleTool(t, tools[1], `{"name":"future","at":"`+future+`","message":"x"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if len(cb.added) != 1 {
		t.Fatal("expected callback")
	}
}

func TestScheduleOnce_PastTime(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	content, isErr := runScheduleTool(t, tools[1], `{"name":"past","at":"2020-01-01T00:00:00Z","message":"x"}`)
	if !isErr {
		t.Fatal("expected error for past time")
	}
	if !strings.Contains(content, "past") {
		t.Errorf("expected past error, got %q", content)
	}
}

func TestScheduleOnce_InvalidTime(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	content, isErr := runScheduleTool(t, tools[1], `{"name":"bad","at":"garbage","message":"x"}`)
	if !isErr {
		t.Fatal("expected error for invalid time")
	}
	if !strings.Contains(content, "invalid time") {
		t.Errorf("expected invalid time error, got %q", content)
	}
}

// --- CancelSchedule tool tests ---

func TestCancelSchedule_Valid(t *testing.T) {
	pdb := openTestPDB(t)
	cb := &fakeScheduleCallback{}
	tools := buildScheduleTools(pdb, cb, time.UTC)

	// Create then cancel.
	runScheduleTool(t, tools[0], `{"name":"to-cancel","schedule":"* * * * *","message":"x"}`)
	content, isErr := runScheduleTool(t, tools[2], `{"name":"to-cancel"}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "cancelled") {
		t.Errorf("expected cancelled response, got %q", content)
	}
	if len(cb.removed) != 1 {
		t.Errorf("expected 1 remove callback, got %d", len(cb.removed))
	}
}

func TestCancelSchedule_NotFound(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	content, isErr := runScheduleTool(t, tools[2], `{"name":"nonexistent"}`)
	if !isErr {
		t.Fatal("expected error for nonexistent schedule")
	}
	if !strings.Contains(content, "no schedule named") {
		t.Errorf("expected not-found error, got %q", content)
	}
}

// --- ListSchedules tool tests ---

func TestListSchedules_Empty(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	content, isErr := runScheduleTool(t, tools[3], `{}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "No active") {
		t.Errorf("expected empty response, got %q", content)
	}
}

func TestListSchedules_WithEntries(t *testing.T) {
	pdb := openTestPDB(t)
	tools := buildScheduleTools(pdb, nil, time.UTC)

	runScheduleTool(t, tools[0], `{"name":"alpha","schedule":"0 9 * * *","message":"a"}`)
	runScheduleTool(t, tools[0], `{"name":"beta","schedule":"*/5 * * * *","message":"b"}`)

	content, isErr := runScheduleTool(t, tools[3], `{}`)
	if isErr {
		t.Fatalf("unexpected error: %s", content)
	}
	if !strings.Contains(content, "alpha") || !strings.Contains(content, "beta") {
		t.Errorf("expected both schedules listed, got %q", content)
	}
	if !strings.Contains(content, "cron") {
		t.Errorf("expected type column, got %q", content)
	}
}

// --- Notify tool tests ---

func TestNotify_PushesNotification(t *testing.T) {
	nq := NewNotificationQueue(testScheduleLogger)
	tool := buildNotifyTool(nq)

	ctx := ContextWithCallerID(context.Background(), "test-inst")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: "Notify", Input: `{"message":"alert!"}`})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}

	items := nq.Drain()
	if len(items) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(items))
	}
	if items[0].Content != "alert!" {
		t.Errorf("expected 'alert!', got %q", items[0].Content)
	}
	if items[0].Source != "scheduled-task" {
		t.Errorf("expected source 'scheduled-task', got %q", items[0].Source)
	}
	if items[0].SessionID != "" {
		t.Error("expected empty SessionID for instance-scoped notification")
	}
}

func TestNotify_EmptyMessage(t *testing.T) {
	nq := NewNotificationQueue(testScheduleLogger)
	tool := buildNotifyTool(nq)

	ctx := ContextWithCallerID(context.Background(), "test-inst")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: "Notify", Input: `{"message":""}`})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError {
		t.Fatal("expected error for empty message")
	}
}

// --- Context provider tests ---

func TestSubscriptionProvider_NoSubscriptions(t *testing.T) {
	pdb := openTestPDB(t)
	provider := SubscriptionProvider(pdb, "test-inst")

	activeTools := map[string]bool{"ScheduleRecurring": true}
	result := provider(activeTools, nil)
	if result != nil {
		t.Error("expected nil for no subscriptions")
	}
}

func TestSubscriptionProvider_WithSubscriptions(t *testing.T) {
	pdb := openTestPDB(t)
	ctx := context.Background()

	nextFire := time.Now().Add(time.Hour).UTC()
	pdb.CreateSubscription(ctx, platformdb.Subscription{
		ID: "sub-1", InstanceID: "test-inst", Name: "daily",
		Trigger: platformdb.TriggerDef{Type: "cron", Expr: "0 9 * * *"},
		Message: "x", Status: "active", NextFire: &nextFire,
	})

	provider := SubscriptionProvider(pdb, "test-inst")
	activeTools := map[string]bool{"ScheduleRecurring": true}
	result := provider(activeTools, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	text := result.Message.Content[0].(fantasy.TextPart).Text
	if !strings.Contains(text, "daily") || !strings.Contains(text, "0 9 * * *") {
		t.Errorf("expected schedule listing, got %q", text)
	}
}

func TestSubscriptionProvider_GatedOnTool(t *testing.T) {
	pdb := openTestPDB(t)
	provider := SubscriptionProvider(pdb, "test-inst")

	// Without ScheduleRecurring in active tools, should return nil.
	activeTools := map[string]bool{"Bash": true}
	result := provider(activeTools, nil)
	if result != nil {
		t.Error("expected nil when ScheduleRecurring not in active tools")
	}
}
