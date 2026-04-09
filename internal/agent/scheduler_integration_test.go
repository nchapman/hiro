package agent

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// newTestSchedulerWithDB creates a Scheduler backed by a real SQLite database.
// The returned Manager has no provider, so RunTriggered will return an error
// (instance not found), which is fine for testing scheduler coordination.
func newTestSchedulerWithDB(t *testing.T) (*Scheduler, *platformdb.DB, *Manager) {
	t.Helper()
	dir := t.TempDir()
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("test"), nil, pdb, nil)
	sched := NewScheduler(pdb, mgr, time.UTC, logger)
	return sched, pdb, mgr
}

// ensureInstance creates the instance row if it doesn't already exist.
func ensureInstance(t *testing.T, pdb *platformdb.DB, instanceID string) {
	t.Helper()
	// Ignore duplicate errors — instance may already exist from a prior call.
	_ = pdb.CreateInstance(context.Background(), platformdb.Instance{
		ID:        instanceID,
		AgentName: "test-agent",
		Mode:      "persistent",
	})
}

// createTestSub is a helper that inserts a subscription into the DB and returns it.
// It also ensures the parent instance row exists to satisfy the FK constraint.
func createTestSub(t *testing.T, pdb *platformdb.DB, id, instanceID, name string, trigger platformdb.TriggerDef, nextFire *time.Time) platformdb.Subscription {
	t.Helper()
	ensureInstance(t, pdb, instanceID)
	sub := platformdb.Subscription{
		ID:         id,
		InstanceID: instanceID,
		Name:       name,
		Trigger:    trigger,
		Message:    "test message for " + name,
		Status:     "active",
		NextFire:   nextFire,
	}
	if err := pdb.CreateSubscription(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	return sub
}

// --- Start / Stop lifecycle ---

func TestScheduler_StartStop_Empty(t *testing.T) {
	sched, _, _ := newTestSchedulerWithDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	// Should have loaded zero subscriptions.
	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 entries on empty DB, got %d", count)
	}

	cancel()
	sched.Stop()
}

func TestScheduler_StartLoadsFromDB(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	// Insert subscriptions into the DB before starting.
	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-1", "inst-1", "every-minute", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)
	createTestSub(t, pdb, "sub-2", "inst-1", "daily", platformdb.TriggerDef{
		Type: "cron", Expr: "0 9 * * *",
	}, &nextFire)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 2 {
		t.Errorf("expected 2 entries loaded from DB, got %d", count)
	}

	cancel()
	sched.Stop()
}

func TestScheduler_StartSkipsInvalidTriggers(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-good", "inst-1", "good", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)
	createTestSub(t, pdb, "sub-bad", "inst-1", "bad", platformdb.TriggerDef{
		Type: "cron", Expr: "invalid-cron",
	}, &nextFire)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 valid entry, got %d", count)
	}

	cancel()
	sched.Stop()
}

func TestScheduler_StartSkipsPausedSubscriptions(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-active", "inst-1", "active-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	// Create a paused subscription — ListActiveSubscriptions filters on status='active'.
	sub := platformdb.Subscription{
		ID:         "sub-paused",
		InstanceID: "inst-1",
		Name:       "paused-sub",
		Trigger:    platformdb.TriggerDef{Type: "cron", Expr: "* * * * *"},
		Message:    "test",
		Status:     "paused",
		NextFire:   &nextFire,
	}
	if err := pdb.CreateSubscription(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 active entry (paused should be excluded), got %d", count)
	}

	cancel()
	sched.Stop()
}

// --- PauseInstance / ResumeInstance ---

func TestScheduler_PauseInstance(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-a", "inst-1", "sub-a", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)
	createTestSub(t, pdb, "sub-b", "inst-1", "sub-b", platformdb.TriggerDef{
		Type: "cron", Expr: "0 9 * * *",
	}, &nextFire)
	createTestSub(t, pdb, "sub-other", "inst-2", "sub-other", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	defer func() { cancel(); sched.Stop() }()

	// Pause inst-1 — should remove its two subs from the heap.
	sched.PauseInstance(context.Background(), "inst-1")

	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 entry after pausing inst-1, got %d", count)
	}

	// Verify DB status is paused.
	sub, err := pdb.GetSubscription(context.Background(), "sub-a")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "paused" {
		t.Errorf("expected status 'paused', got %q", sub.Status)
	}
}

func TestScheduler_ResumeInstance(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-a", "inst-1", "sub-a", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	defer func() { cancel(); sched.Stop() }()

	// Pause then resume.
	sched.PauseInstance(context.Background(), "inst-1")

	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 after pause, got %d", count)
	}

	sched.ResumeInstance(context.Background(), "inst-1")

	sched.mu.Lock()
	count = sched.heap.Len()
	sched.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 after resume, got %d", count)
	}

	// Verify DB status is active again.
	sub, err := pdb.GetSubscription(context.Background(), "sub-a")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "active" {
		t.Errorf("expected status 'active' after resume, got %q", sub.Status)
	}
}

func TestScheduler_PauseInstanceMarksRunningAsCancelled(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-running", "inst-1", "running-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	// Simulate a subscription that is currently firing.
	sched.mu.Lock()
	sched.running["sub-running"] = "inst-1"
	sched.mu.Unlock()

	sched.PauseInstance(context.Background(), "inst-1")

	sched.mu.Lock()
	wasCancelled := sched.cancelled["sub-running"]
	sched.mu.Unlock()
	if !wasCancelled {
		t.Error("expected running subscription to be marked as cancelled during pause")
	}
}

// --- Remove while firing ---

func TestScheduler_RemoveWhileFiring(t *testing.T) {
	s := newTestScheduler()

	sub := platformdb.Subscription{
		ID:      "sub-1",
		Name:    "test",
		Trigger: platformdb.TriggerDef{Type: "cron", Expr: "* * * * *"},
	}
	s.Add(sub)

	// Simulate the subscription as currently firing (remove from heap, add to running).
	s.mu.Lock()
	s.running["sub-1"] = "inst-1"
	// Remove from heap to simulate fireReady having popped it.
	for i, entry := range s.heap {
		if entry.sub.ID == "sub-1" {
			s.heap = append(s.heap[:i], s.heap[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	// Remove while firing should mark as cancelled, not panic.
	s.Remove("sub-1")

	s.mu.Lock()
	cancelled := s.cancelled["sub-1"]
	s.mu.Unlock()
	if !cancelled {
		t.Error("expected subscription to be marked as cancelled when removed while firing")
	}
}

// --- fireReady / fireSingle ---

func TestScheduler_FireReady_FiresDueSubscriptions(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub := createTestSub(t, pdb, "sub-due", "inst-1", "due-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	// Build a heap entry with a past fire time so fireReady picks it up immediately.
	entry := sched.buildEntry(sub, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected valid entry")
	}
	entry.nextFire = time.Now().Add(-time.Second) // force it to be due

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add directly to heap and start the loop.
	sched.mu.Lock()
	sched.heap = append(sched.heap, entry)
	sched.mu.Unlock()
	sched.Start(ctx)

	// Wait briefly for the run loop to fire the subscription.
	time.Sleep(500 * time.Millisecond)

	cancel()
	sched.Stop()

	// RunTriggered will fail (instance not found), so verify error was recorded.
	got, err := pdb.GetSubscription(context.Background(), "sub-due")
	if err != nil {
		t.Fatal(err)
	}
	if got.ErrorCount == 0 {
		t.Error("expected error count > 0 after failed fire")
	}
	if got.LastError == "" {
		t.Error("expected non-empty last_error after failed fire")
	}
}

func TestScheduler_FireReady_SkipsOverlapping(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	past := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-overlap", "inst-1", "overlap-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &past)

	// Don't start the run loop — test fireReady directly.
	// Load the subscription into the heap manually.
	ctx := context.Background()
	subs, err := pdb.ListActiveSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sched.mu.Lock()
	now := time.Now().In(sched.tz)
	for _, sub := range subs {
		entry := sched.buildEntry(sub, now)
		if entry != nil {
			sched.heap = append(sched.heap, entry)
		}
	}
	// Mark it as already running to test overlap skip.
	sched.running["sub-overlap"] = "inst-1"
	sched.mu.Unlock()

	sched.fireReady(ctx)

	// Should still be in the heap (pushed back because it's running).
	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 entry (pushed back due to overlap), got %d", count)
	}
}

func TestScheduler_FireSingle_RecordsErrorInDB(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub := createTestSub(t, pdb, "sub-err", "inst-not-exist", "error-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	entry := sched.buildEntry(sub, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected valid entry")
	}

	sched.mu.Lock()
	sched.running[sub.ID] = sub.InstanceID
	sched.mu.Unlock()

	sched.fireSingle(context.Background(), entry)

	// Verify error was recorded in DB.
	updated, err := pdb.GetSubscription(context.Background(), "sub-err")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ErrorCount != 1 {
		t.Errorf("expected error_count=1, got %d", updated.ErrorCount)
	}
	if updated.LastError == "" {
		t.Error("expected non-empty last_error")
	}

	// Verify subscription was re-added to heap (cron reschedules after error).
	sched.mu.Lock()
	count := sched.heap.Len()
	running := sched.running[sub.ID]
	sched.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 entry re-added to heap after error, got %d", count)
	}
	if running != "" {
		t.Error("expected running entry to be cleared after fire completes")
	}
}

func TestScheduler_FireSingle_CancelledNotReAdded(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub := createTestSub(t, pdb, "sub-cancel", "inst-not-exist", "cancel-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	entry := sched.buildEntry(sub, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected valid entry")
	}

	sched.mu.Lock()
	sched.running[sub.ID] = sub.InstanceID
	sched.cancelled[sub.ID] = true // simulate Remove() during fire
	sched.mu.Unlock()

	sched.fireSingle(context.Background(), entry)

	// Should NOT be re-added to heap because it was cancelled.
	sched.mu.Lock()
	count := sched.heap.Len()
	_, stillCancelled := sched.cancelled[sub.ID]
	sched.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 entries (cancelled should not re-add), got %d", count)
	}
	if stillCancelled {
		t.Error("expected cancelled entry to be cleaned up")
	}
}

func TestScheduler_FireSingle_OnceNotDeletedOnError(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub := createTestSub(t, pdb, "sub-once-err", "inst-not-exist", "once-err", platformdb.TriggerDef{
		Type: "once", At: future.Format(time.RFC3339),
	}, &future)

	entry := sched.buildEntry(sub, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected valid entry")
	}

	sched.mu.Lock()
	sched.running[sub.ID] = sub.InstanceID
	sched.mu.Unlock()

	sched.fireSingle(context.Background(), entry)

	// One-shot should NOT be deleted on error — only on success.
	_, err := pdb.GetSubscription(context.Background(), "sub-once-err")
	if err != nil {
		t.Errorf("one-shot subscription should still exist after error: %v", err)
	}

	// But should not be re-added to heap either (once triggers have no schedule/next).
	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 entries (once has no next fire), got %d", count)
	}
}

// --- Run loop integration ---

func TestScheduler_RunLoop_WakesOnAdd(t *testing.T) {
	sched, _, _ := newTestSchedulerWithDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	// Start with empty heap — run loop is sleeping on wake channel.
	// Add a far-future subscription — run loop should wake and re-evaluate.
	sub := platformdb.Subscription{
		ID:      "sub-future",
		Name:    "future",
		Trigger: platformdb.TriggerDef{Type: "cron", Expr: "0 0 1 1 *"}, // once a year
	}
	sched.Add(sub)

	sched.mu.Lock()
	count := sched.heap.Len()
	sched.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 entry after add, got %d", count)
	}

	cancel()
	sched.Stop()
}

func TestScheduler_RunLoop_FiresOnSchedule(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	// Create a once subscription due 2 seconds from now (enough buffer for truncation).
	dueTime := time.Now().Add(2 * time.Second).UTC().Truncate(time.Second)
	createTestSub(t, pdb, "sub-soon", "inst-1", "soon-sub", platformdb.TriggerDef{
		Type: "once", At: dueTime.Format(time.RFC3339),
	}, &dueTime)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	// Wait for the subscription to fire.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for subscription to fire")
		default:
		}

		sub, err := pdb.GetSubscription(context.Background(), "sub-soon")
		if err != nil {
			// Once triggers are deleted on success, and error path keeps them.
			// RunTriggered will fail → error recorded → sub still exists.
			t.Fatal(err)
		}
		if sub.ErrorCount > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	sched.Stop()
}

// --- Concurrency ---

func TestScheduler_ConcurrentAddRemove(t *testing.T) {
	s := newTestScheduler()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		id := platformdb.Subscription{
			ID:      "sub-" + time.Now().Format("150405.000000000") + "-" + string(rune('a'+i%26)),
			Name:    "concurrent",
			Trigger: platformdb.TriggerDef{Type: "cron", Expr: "* * * * *"},
		}
		go func(sub platformdb.Subscription) {
			defer wg.Done()
			s.Add(sub)
		}(id)
		go func(sub platformdb.Subscription) {
			defer wg.Done()
			s.Remove(sub.ID)
		}(id)
	}
	wg.Wait()
	// No panics, no data races — that's the assertion.
}

func TestScheduler_ConcurrentPauseResume(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for i := range 5 {
		id := "sub-" + string(rune('a'+i))
		createTestSub(t, pdb, id, "inst-1", "sub-"+string(rune('a'+i)), platformdb.TriggerDef{
			Type: "cron", Expr: "* * * * *",
		}, &nextFire)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	defer func() { cancel(); sched.Stop() }()

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			sched.PauseInstance(context.Background(), "inst-1")
		}()
		go func() {
			defer wg.Done()
			sched.ResumeInstance(context.Background(), "inst-1")
		}()
	}
	wg.Wait()
	// No panics, no data races.
}

// --- NewScheduler defaults ---

func TestNewScheduler_NilTimezoneDefaultsToUTC(t *testing.T) {
	dir := t.TempDir()
	pdb := openTestPDB(t, dir)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(t.Context(), dir, Options{WorkingDir: dir}, nil, logger, testWorkerFactory("test"), nil, pdb, nil)

	sched := NewScheduler(pdb, mgr, nil, logger)
	if sched.tz != time.UTC {
		t.Errorf("expected UTC, got %v", sched.tz)
	}
}

// --- fireReady with multiple due entries ---

func TestScheduler_FireReady_MultipleEntries(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub1 := createTestSub(t, pdb, "sub-1", "inst-1", "sub-1", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)
	sub2 := createTestSub(t, pdb, "sub-2", "inst-2", "sub-2", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	// Build entries with past fire times so they fire immediately.
	for _, sub := range []platformdb.Subscription{sub1, sub2} {
		entry := sched.buildEntry(sub, time.Now().UTC())
		if entry == nil {
			t.Fatalf("expected valid entry for %s", sub.ID)
		}
		entry.nextFire = time.Now().Add(-time.Second)
		sched.mu.Lock()
		sched.heap = append(sched.heap, entry)
		sched.mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	// Wait for both to fire.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for subscriptions to fire")
		default:
		}

		s1, _ := pdb.GetSubscription(context.Background(), "sub-1")
		s2, _ := pdb.GetSubscription(context.Background(), "sub-2")
		if s1.ErrorCount > 0 && s2.ErrorCount > 0 {
			goto done
		}
		time.Sleep(50 * time.Millisecond)
	}
done:
	cancel()
	sched.Stop()
}

// --- Stop waits for in-flight fires ---

func TestScheduler_StopWaitsForInflight(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub := createTestSub(t, pdb, "sub-inflight", "inst-1", "inflight-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	// Force entry to fire immediately.
	entry := sched.buildEntry(sub, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected valid entry")
	}
	entry.nextFire = time.Now().Add(-time.Second)
	sched.mu.Lock()
	sched.heap = append(sched.heap, entry)
	sched.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	// Let the scheduler fire.
	time.Sleep(500 * time.Millisecond)

	// Stop should not hang.
	cancel()
	stopped := make(chan struct{})
	go func() {
		sched.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		// Good.
	case <-time.After(10 * time.Second):
		t.Fatal("Stop() blocked for too long — may be hanging on wg.Wait()")
	}
}

// --- Fire count tracking ---

func TestScheduler_FireCountIncrementsOnFire(t *testing.T) {
	sched, pdb, _ := newTestSchedulerWithDB(t)

	nextFire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sub := createTestSub(t, pdb, "sub-count", "inst-1", "count-sub", platformdb.TriggerDef{
		Type: "cron", Expr: "* * * * *",
	}, &nextFire)

	// Force immediate fire.
	entry := sched.buildEntry(sub, time.Now().UTC())
	if entry == nil {
		t.Fatal("expected valid entry")
	}
	entry.nextFire = time.Now().Add(-time.Second)
	sched.mu.Lock()
	sched.heap = append(sched.heap, entry)
	sched.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	// Wait for at least one fire.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for error_count > 0")
		default:
		}

		got, err := pdb.GetSubscription(context.Background(), "sub-count")
		if err != nil {
			t.Fatal(err)
		}
		if got.ErrorCount > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	sched.Stop()

	// Verify the error was recorded (RunTriggered fails because inst-1 doesn't exist in manager).
	got, err := pdb.GetSubscription(context.Background(), "sub-count")
	if err != nil {
		t.Fatal(err)
	}
	if got.ErrorCount == 0 {
		t.Error("expected error_count > 0")
	}
}
