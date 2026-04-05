package agent

import (
	"container/heap"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

func newTestScheduler() *Scheduler {
	return &Scheduler{
		running:   make(map[string]string),
		cancelled: make(map[string]bool),
		parser:    cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		tz:        time.UTC,
		logger:    testLogger,
		done:      make(chan struct{}),
		wake:      make(chan struct{}, 1),
	}
}

// --- computeNextFire / buildEntry tests ---

func TestComputeNextFire_Cron(t *testing.T) {
	s := newTestScheduler()

	trigger := platformdb.TriggerDef{Type: "cron", Expr: "* * * * *"}
	now := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	next := s.computeNextFire(trigger, now)
	if next == nil {
		t.Fatal("expected non-nil next fire")
	}
	expected := time.Date(2026, 4, 5, 12, 1, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestComputeNextFire_DailyAt9(t *testing.T) {
	s := newTestScheduler()

	trigger := platformdb.TriggerDef{Type: "cron", Expr: "0 9 * * *"}
	now := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC) // after 9am, so next is tomorrow
	next := s.computeNextFire(trigger, now)
	if next == nil {
		t.Fatal("expected non-nil next fire")
	}
	expected := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestComputeNextFire_InvalidCron(t *testing.T) {
	s := newTestScheduler()

	trigger := platformdb.TriggerDef{Type: "cron", Expr: "invalid"}
	next := s.computeNextFire(trigger, time.Now())
	if next != nil {
		t.Errorf("expected nil for invalid expr, got %v", *next)
	}
}

func TestComputeNextFire_NonCronType(t *testing.T) {
	s := newTestScheduler()

	trigger := platformdb.TriggerDef{Type: "webhook"}
	next := s.computeNextFire(trigger, time.Now())
	if next != nil {
		t.Errorf("expected nil for non-cron type, got %v", *next)
	}
}

// --- Once trigger tests ---

func TestBuildEntry_OnceFuture(t *testing.T) {
	s := newTestScheduler()

	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	trigger := platformdb.TriggerDef{Type: "once", At: future.Format(time.RFC3339)}
	entry := s.buildEntry(platformdb.Subscription{Trigger: trigger}, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected non-nil entry for future once trigger")
	}
	if !entry.nextFire.Equal(future) {
		t.Errorf("expected %v, got %v", future, entry.nextFire)
	}
	if entry.schedule != nil {
		t.Error("expected nil schedule for once trigger")
	}
}

func TestBuildEntry_OncePast(t *testing.T) {
	s := newTestScheduler()

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	trigger := platformdb.TriggerDef{Type: "once", At: past}
	entry := s.buildEntry(platformdb.Subscription{Trigger: trigger}, time.Now().UTC())
	if entry != nil {
		t.Error("expected nil entry for past once trigger")
	}
}

func TestBuildEntry_OnceInvalidTime(t *testing.T) {
	s := newTestScheduler()

	trigger := platformdb.TriggerDef{Type: "once", At: "not-a-time"}
	entry := s.buildEntry(platformdb.Subscription{Trigger: trigger}, time.Now().UTC())
	if entry != nil {
		t.Error("expected nil entry for invalid time")
	}
}

func TestBuildEntry_CronCachesSchedule(t *testing.T) {
	s := newTestScheduler()

	trigger := platformdb.TriggerDef{Type: "cron", Expr: "*/5 * * * *"}
	entry := s.buildEntry(platformdb.Subscription{Trigger: trigger}, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected entry")
	}
	if entry.schedule == nil {
		t.Error("expected cached schedule for cron trigger")
	}
}

// --- Heap tests ---

func TestSubHeap_Ordering(t *testing.T) {
	now := time.Now()
	h := &subHeap{}
	heap.Init(h)

	entries := []*subEntry{
		{sub: platformdb.Subscription{ID: "c"}, nextFire: now.Add(3 * time.Hour)},
		{sub: platformdb.Subscription{ID: "a"}, nextFire: now.Add(1 * time.Hour)},
		{sub: platformdb.Subscription{ID: "b"}, nextFire: now.Add(2 * time.Hour)},
	}
	for _, e := range entries {
		heap.Push(h, e)
	}

	if h.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", h.Len())
	}

	first := heap.Pop(h).(*subEntry) //nolint:forcetypeassert
	if first.sub.ID != "a" {
		t.Errorf("expected a first, got %s", first.sub.ID)
	}
	second := heap.Pop(h).(*subEntry) //nolint:forcetypeassert
	if second.sub.ID != "b" {
		t.Errorf("expected b second, got %s", second.sub.ID)
	}
	third := heap.Pop(h).(*subEntry) //nolint:forcetypeassert
	if third.sub.ID != "c" {
		t.Errorf("expected c third, got %s", third.sub.ID)
	}
}

func TestSubHeap_Remove(t *testing.T) {
	now := time.Now()
	h := &subHeap{}
	heap.Init(h)

	heap.Push(h, &subEntry{sub: platformdb.Subscription{ID: "a"}, nextFire: now.Add(1 * time.Hour)})
	heap.Push(h, &subEntry{sub: platformdb.Subscription{ID: "b"}, nextFire: now.Add(2 * time.Hour)})
	heap.Push(h, &subEntry{sub: platformdb.Subscription{ID: "c"}, nextFire: now.Add(3 * time.Hour)})

	// Remove middle entry.
	for i, e := range *h {
		if e.sub.ID == "b" {
			heap.Remove(h, i)
			break
		}
	}

	if h.Len() != 2 {
		t.Fatalf("expected 2 entries after remove, got %d", h.Len())
	}
	first := heap.Pop(h).(*subEntry) //nolint:forcetypeassert
	if first.sub.ID != "a" {
		t.Errorf("expected a first after remove, got %s", first.sub.ID)
	}
}

func TestSubHeap_Empty(t *testing.T) {
	h := &subHeap{}
	heap.Init(h)
	if h.Len() != 0 {
		t.Errorf("expected 0, got %d", h.Len())
	}
}

// --- Scheduler Add/Remove tests ---

func TestScheduler_AddRemove(t *testing.T) {
	s := newTestScheduler()

	sub := platformdb.Subscription{
		ID:      "sub-1",
		Name:    "test",
		Trigger: platformdb.TriggerDef{Type: "cron", Expr: "* * * * *"},
	}
	s.Add(sub)

	s.mu.Lock()
	count := s.heap.Len()
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 entry after add, got %d", count)
	}

	s.Remove("sub-1")
	s.mu.Lock()
	count = s.heap.Len()
	s.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 entries after remove, got %d", count)
	}
}

func TestScheduler_RemoveNonexistent(t *testing.T) {
	s := newTestScheduler()
	// Should not panic.
	s.Remove("no-such-id")
}

func TestScheduler_AddInvalidTrigger(t *testing.T) {
	s := newTestScheduler()
	sub := platformdb.Subscription{
		ID:      "sub-bad",
		Trigger: platformdb.TriggerDef{Type: "cron", Expr: "invalid"},
	}
	s.Add(sub)

	s.mu.Lock()
	count := s.heap.Len()
	s.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 entries for invalid trigger, got %d", count)
	}
}

func TestScheduler_AddOnceTrigger(t *testing.T) {
	s := newTestScheduler()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	sub := platformdb.Subscription{
		ID:      "sub-once",
		Name:    "once-test",
		Trigger: platformdb.TriggerDef{Type: "once", At: future},
	}
	s.Add(sub)

	s.mu.Lock()
	count := s.heap.Len()
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 entry for once trigger, got %d", count)
	}
}

// --- Signal/wake channel tests ---

func TestScheduler_SignalNonBlocking(t *testing.T) {
	s := newTestScheduler()

	// Multiple signals should not block.
	s.signal()
	s.signal()
	s.signal()

	// Drain the channel.
	select {
	case <-s.wake:
	default:
		t.Error("expected wake signal")
	}

	// Should be empty now.
	select {
	case <-s.wake:
		t.Error("expected empty wake channel")
	default:
	}
}
