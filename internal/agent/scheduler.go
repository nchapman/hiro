package agent

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/nchapman/hiro/internal/config"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// triggeredTurnTimeout caps the wall-clock duration of a single triggered
// inference turn. Prevents a hung LLM call from blocking the user's primary
// session indefinitely (RunTriggered holds inst.mu).
const triggeredTurnTimeout = 10 * time.Minute

// Scheduler manages cron subscriptions and fires them on schedule.
// It runs a single goroutine that sleeps until the next subscription is due.
// Subscriptions fire in separate goroutines so a slow turn doesn't block others.
type Scheduler struct {
	mu        sync.Mutex
	heap      subHeap
	running   map[string]string // sub ID -> instance ID for currently-firing subscriptions
	cancelled map[string]bool   // sub IDs removed/paused while firing — prevents re-add
	wg        sync.WaitGroup    // tracks in-flight fireSingle goroutines
	mgr       *Manager
	pdb       *platformdb.DB
	parser    cron.Parser
	tz        *time.Location
	logger    *slog.Logger
	done      chan struct{}
	wake      chan struct{} // signaled when heap changes
	cancel    context.CancelFunc
}

// NewScheduler creates a scheduler. Call Start to begin processing.
func NewScheduler(pdb *platformdb.DB, mgr *Manager, tz *time.Location, logger *slog.Logger) *Scheduler {
	if tz == nil {
		tz = time.UTC
	}
	return &Scheduler{
		running:   make(map[string]string),
		cancelled: make(map[string]bool),
		pdb:       pdb,
		mgr:       mgr,
		parser:    cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		tz:        tz,
		logger:    logger.With("component", "scheduler"),
		done:      make(chan struct{}),
		wake:      make(chan struct{}, 1),
	}
}

// Start loads active subscriptions from the database and starts the timer loop.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)

	subs, err := s.pdb.ListActiveSubscriptions(ctx)
	if err != nil {
		s.logger.Error("failed to load subscriptions", "error", err)
		close(s.done)
		return
	}

	s.mu.Lock()
	now := time.Now().In(s.tz)
	for _, sub := range subs {
		entry := s.buildEntry(sub, now)
		if entry == nil {
			continue
		}
		s.heap = append(s.heap, entry)
	}
	heap.Init(&s.heap)
	s.mu.Unlock()

	s.logger.Info("scheduler started", "active_subscriptions", len(s.heap), "timezone", s.tz.String())
	go s.run(ctx)
}

// Stop cancels the scheduler and waits for all in-flight fires to finish.
func (s *Scheduler) Stop() {
	s.logger.Info("scheduler stopping")
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done    // run loop exited
	s.wg.Wait() // all fireSingle goroutines finished
	s.logger.Info("scheduler stopped")
}

// Add inserts a subscription into the scheduler.
func (s *Scheduler) Add(sub platformdb.Subscription) {
	entry := s.buildEntry(sub, time.Now().In(s.tz))
	if entry == nil {
		return
	}
	s.mu.Lock()
	heap.Push(&s.heap, entry)
	s.mu.Unlock()
	s.logger.Info("subscription added",
		"sub_id", sub.ID, "name", sub.Name,
		"type", sub.Trigger.Type, "next_fire", entry.nextFire)
	s.signal()
}

// Remove removes a subscription from the scheduler by ID.
// If the subscription is currently firing, it is marked as cancelled so
// fireSingle won't re-add it to the heap when it completes.
func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	removed := false
	for i, entry := range s.heap {
		if entry.sub.ID == id {
			s.logger.Info("subscription removed", "sub_id", id, "name", entry.sub.Name)
			heap.Remove(&s.heap, i)
			removed = true
			break
		}
	}
	if !removed && s.running[id] != "" {
		s.cancelled[id] = true
		s.logger.Info("subscription cancelled (currently firing)", "sub_id", id)
	}
	s.mu.Unlock()
	s.signal()
}

// PauseInstance pauses all subscriptions for an instance.
func (s *Scheduler) PauseInstance(ctx context.Context, instanceID string) {
	s.logger.Info("pausing instance subscriptions", "instance_id", instanceID)
	if err := s.pdb.PauseInstanceSubscriptions(ctx, instanceID); err != nil {
		s.logger.Error("failed to pause subscriptions", "instance_id", instanceID, "error", err)
		return
	}
	s.mu.Lock()
	var remaining subHeap
	for _, entry := range s.heap {
		if entry.sub.InstanceID != instanceID {
			remaining = append(remaining, entry)
		}
	}
	// Mark any currently-firing subscriptions for this instance as cancelled.
	for subID, instID := range s.running {
		if instID == instanceID {
			s.cancelled[subID] = true
		}
	}
	s.heap = remaining
	heap.Init(&s.heap)
	s.mu.Unlock()
	s.signal()
}

// ResumeInstance reactivates subscriptions for an instance.
func (s *Scheduler) ResumeInstance(ctx context.Context, instanceID string) {
	s.logger.Info("resuming instance subscriptions", "instance_id", instanceID)
	subs, err := s.pdb.ResumeInstanceSubscriptions(ctx, instanceID)
	if err != nil {
		s.logger.Error("failed to resume subscriptions", "instance_id", instanceID, "error", err)
		return
	}
	now := time.Now().In(s.tz)

	// Build entries and update DB outside the heap lock.
	var entries []*subEntry
	for _, sub := range subs {
		entry := s.buildEntry(sub, now)
		if entry == nil {
			continue
		}
		if err := s.pdb.UpdateSubscriptionStatus(context.Background(), sub.ID, "active", &entry.nextFire); err != nil {
			s.logger.Error("failed to update next_fire", "sub_id", sub.ID, "error", err)
		}
		entries = append(entries, entry)
	}

	s.mu.Lock()
	for _, e := range entries {
		heap.Push(&s.heap, e)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *Scheduler) run(ctx context.Context) {
	defer close(s.done)

	for {
		s.mu.Lock()
		empty := s.heap.Len() == 0
		var next time.Time
		if !empty {
			next = s.heap[0].nextFire
		}
		s.mu.Unlock()

		if empty {
			// No subscriptions — wait for a wake signal or cancel.
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}

		delay := time.Until(next)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.wake:
				timer.Stop()
				continue
			case <-timer.C:
			}
		}

		s.fireReady(ctx)
	}
}

// fireReady fires all subscriptions that are due. Each fires in its own
// goroutine so a slow turn doesn't block other subscriptions. A per-subscription
// guard prevents overlapping fires.
func (s *Scheduler) fireReady(ctx context.Context) {
	now := time.Now().In(s.tz)

	for {
		s.mu.Lock()
		if s.heap.Len() == 0 || s.heap[0].nextFire.After(now) {
			s.mu.Unlock()
			return
		}
		popped := heap.Pop(&s.heap)
		entry, ok := popped.(*subEntry)
		if !ok {
			s.mu.Unlock()
			continue
		}

		subID := entry.sub.ID

		// Skip if this subscription is already running (previous fire still in progress).
		// Break instead of continue to avoid spinning when the soonest entry is overlapping.
		if s.running[subID] != "" {
			heap.Push(&s.heap, entry)
			s.mu.Unlock()
			s.logger.Debug("skipping overlapping fire", "sub_id", subID, "name", entry.sub.Name)
			break
		}
		s.running[subID] = entry.sub.InstanceID
		s.mu.Unlock()

		s.wg.Go(func() {
			s.fireSingle(ctx, entry)
		})
	}
}

func (s *Scheduler) fireSingle(ctx context.Context, entry *subEntry) {
	sub := entry.sub
	logger := s.logger.With("sub_id", sub.ID, "name", sub.Name, "instance_id", sub.InstanceID)
	logger.Info("firing subscription")

	firedAt := time.Now().In(s.tz)
	err := s.mgr.RunTriggered(ctx, sub)

	// Compute next fire time from now (after inference), skipping missed intervals.
	var next *time.Time
	if entry.schedule != nil {
		t := entry.schedule.Next(time.Now().In(s.tz))
		next = &t
	}

	if err != nil {
		logger.Error("subscription fire failed", "error", err)
		if dbErr := s.pdb.UpdateSubscriptionError(ctx, sub.ID, next, err.Error()); dbErr != nil {
			logger.Error("failed to record fire error", "error", dbErr)
		}
	} else {
		logger.Info("subscription fired successfully")
		if dbErr := s.pdb.UpdateSubscriptionFired(ctx, sub.ID, firedAt, next); dbErr != nil {
			logger.Error("failed to record fire success", "error", dbErr)
		}
	}

	// One-shot triggers: clean up subscription after successful fire.
	if sub.Trigger.Type == "once" && err == nil {
		logger.Info("one-shot subscription completed, removing")
		if dbErr := s.pdb.DeleteSubscription(ctx, sub.ID); dbErr != nil {
			logger.Error("failed to delete one-shot subscription", "error", dbErr)
		}
		next = nil // don't re-add
	}

	s.mu.Lock()
	delete(s.running, sub.ID)
	// Check if this subscription was removed/paused while we were firing.
	wasCancelled := s.cancelled[sub.ID]
	delete(s.cancelled, sub.ID)
	// Re-add to heap only if there's a next fire time and not cancelled.
	if next != nil && !wasCancelled {
		entry.nextFire = *next
		heap.Push(&s.heap, entry)
	}
	s.mu.Unlock()

	// Signal the run loop to re-evaluate (next fire time may have changed).
	s.signal()
}

// buildEntry creates a subEntry from a subscription. Returns nil if the
// trigger is invalid or a one-shot that has already passed.
func (s *Scheduler) buildEntry(sub platformdb.Subscription, now time.Time) *subEntry {
	switch sub.Trigger.Type {
	case "cron":
		sched, err := s.parser.Parse(sub.Trigger.Expr)
		if err != nil {
			s.logger.Error("invalid cron expression", "expr", sub.Trigger.Expr, "error", err)
			return nil
		}
		return &subEntry{sub: sub, schedule: sched, nextFire: sched.Next(now)}

	case "once":
		t, err := time.Parse(time.RFC3339, sub.Trigger.At)
		if err != nil {
			s.logger.Error("invalid once trigger time", "at", sub.Trigger.At, "error", err)
			return nil
		}
		if !t.After(now) {
			// Already past — clean up.
			s.logger.Info("once trigger already past, removing", "sub_id", sub.ID, "at", sub.Trigger.At)
			if s.pdb != nil {
				_ = s.pdb.DeleteSubscription(context.Background(), sub.ID)
			}
			return nil
		}
		return &subEntry{sub: sub, nextFire: t}

	default:
		return nil
	}
}

// computeNextFire is a convenience for tests.
func (s *Scheduler) computeNextFire(trigger platformdb.TriggerDef, after time.Time) *time.Time {
	entry := s.buildEntry(platformdb.Subscription{Trigger: trigger}, after)
	if entry == nil {
		return nil
	}
	return &entry.nextFire
}

// signal wakes the run loop so it re-evaluates the heap.
func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// RunTriggered runs an inference turn in a triggered session for a subscription.
// Holds inst.mu for the full duration to serialize with SendMessage/SendMetaMessage,
// because the worker process cannot safely handle concurrent tool execution (shared
// env vars, CWD, and background task registry).
func (m *Manager) RunTriggered(ctx context.Context, sub platformdb.Subscription) error {
	inst := m.getInstance(sub.InstanceID)
	if inst == nil {
		return fmt.Errorf("instance %s not found", sub.InstanceID)
	}

	inst.mu.Lock()
	defer inst.mu.Unlock()

	if inst.info.Status == InstanceStatusStopped {
		return fmt.Errorf("instance %s is stopped", sub.InstanceID)
	}
	if inst.loop == nil {
		return fmt.Errorf("instance %s has no inference loop", sub.InstanceID)
	}

	logger := m.logger.With("instance_id", sub.InstanceID, "sub_id", sub.ID, "sub_name", sub.Name)
	logger.Info("running triggered session")

	// Get or create the triggered session.
	sessionID, sessionDir, err := m.ensureTriggeredSession(ctx, inst, sub.ID)
	if err != nil {
		return fmt.Errorf("ensuring triggered session: %w", err)
	}
	logger.Info("triggered session ready", "session_id", sessionID)

	cfg, err := config.LoadAgentDir(m.agentDefDir(inst.agentName))
	if err != nil {
		return fmt.Errorf("loading agent config: %w", err)
	}

	hasSkills := m.agentHasSkills(cfg)
	allowedTools := buildAllowedToolsMap(inst.effectiveTools, inst.info.Mode, hasSkills)

	modelSpec, apiKey, baseURL, err := m.resolveModelSpec(cfg.Model)
	if err != nil {
		return fmt.Errorf("resolving model: %w", err)
	}

	loopCfg := m.buildLoopConfig(
		sub.InstanceID, sessionID, cfg, inst.info.Mode,
		m.instanceDir(sub.InstanceID), sessionDir,
		inst.worker, allowedTools, inst.allowLayers, inst.denyRules,
		hasSkills, modelSpec, inst.notifications,
	)
	loopCfg.IsTriggeredSession = true

	tmpLoop, err := m.createInferenceLoop(ctx, loopCfg, modelSpec, apiKey, baseURL)
	if err != nil {
		return fmt.Errorf("creating triggered loop: %w", err)
	}
	if tmpLoop == nil {
		return fmt.Errorf("no provider configured")
	}

	triggerCtx, triggerCancel := context.WithTimeout(ctx, triggeredTurnTimeout)
	defer triggerCancel()

	_, err = tmpLoop.Chat(triggerCtx, sub.Message, nil, nil)
	return err
}

// ensureTriggeredSession finds or creates the session for a subscription.
func (m *Manager) ensureTriggeredSession(ctx context.Context, inst *instance, subscriptionID string) (string, string, error) {
	if m.pdb == nil {
		return "", "", fmt.Errorf("no platform database")
	}

	// Look for an existing triggered session.
	sess, found, err := m.pdb.SessionBySubscription(ctx, subscriptionID)
	if err != nil {
		return "", "", err
	}
	if found {
		sessDir := m.instanceSessionDir(inst.info.ID, sess.ID)
		if err := createSessionDirs(sessDir); err != nil {
			return "", "", err
		}
		m.logger.Debug("reusing triggered session", "subscription_id", subscriptionID, "session_id", sess.ID)
		return sess.ID, sessDir, nil
	}

	// Create a new session for this subscription.
	m.logger.Info("creating triggered session", "subscription_id", subscriptionID, "instance_id", inst.info.ID)
	sessionID := uuid.Must(uuid.NewV7()).String()
	sessDir := m.instanceSessionDir(inst.info.ID, sessionID)
	if err := createSessionDirs(sessDir); err != nil {
		return "", "", err
	}

	if err := m.pdb.CreateSession(ctx, platformdb.Session{
		ID:             sessionID,
		InstanceID:     inst.info.ID,
		AgentName:      inst.agentName,
		Mode:           string(inst.info.Mode),
		SubscriptionID: subscriptionID,
	}); err != nil {
		return "", "", fmt.Errorf("creating triggered session: %w", err)
	}

	return sessionID, sessDir, nil
}

// --- min-heap for subscription scheduling ---

type subEntry struct {
	sub      platformdb.Subscription
	schedule cron.Schedule // parsed cron expression, cached at insertion
	nextFire time.Time
	index    int
}

type subHeap []*subEntry

func (h subHeap) Len() int           { return len(h) }
func (h subHeap) Less(i, j int) bool { return h[i].nextFire.Before(h[j].nextFire) }
func (h subHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *subHeap) Push(x any) {
	entry, ok := x.(*subEntry)
	if !ok {
		return
	}
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *subHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*h = old[:n-1]
	return entry
}
