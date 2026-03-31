package inference

import (
	"log/slog"
	"sync"
)

// maxNotifications is the maximum number of pending notifications before
// oldest items are dropped. Prevents unbounded memory growth when no
// consumer is connected.
const maxNotifications = 1000

// Notification is a message injected into an inference loop without user input.
// Notifications are stored as isMeta user messages — visible to the model but
// hidden from the user's transcript.
type Notification struct {
	// Content is the message text delivered to the model.
	Content string

	// Source identifies the producer (e.g. "task-completion", "cron", "webhook").
	// Used for logging and future routing/filtering.
	Source string

	// SessionID scopes the notification to a specific session. If set, the
	// notification is discarded during drain if the active session doesn't
	// match. If empty, the notification is delivered to whatever session is
	// current (instance-scoped).
	SessionID string
}

// NotificationQueue is a thread-safe queue for injecting messages into an
// inference loop. Producers call Push from any goroutine; consumers watch
// Ready and call Drain to collect pending items.
//
// The queue is intentionally simple — it stores notifications in order and
// signals availability. Drain policy (batching, prioritization, rate limiting)
// is the caller's responsibility.
type NotificationQueue struct {
	mu     sync.Mutex
	items  []Notification
	ch     chan struct{} // buffered(1), signaled on push
	logger *slog.Logger
}

// NewNotificationQueue creates an empty queue.
func NewNotificationQueue(logger *slog.Logger) *NotificationQueue {
	return &NotificationQueue{
		ch:     make(chan struct{}, 1),
		logger: logger,
	}
}

// Push adds a notification to the queue and signals any waiters. If the queue
// is at capacity, the oldest notification is dropped.
func (q *NotificationQueue) Push(n Notification) {
	q.mu.Lock()
	if len(q.items) >= maxNotifications {
		dropped := q.items[0]
		q.items = q.items[1:]
		q.logger.Warn("notification dropped (queue full)",
			"dropped_source", dropped.Source,
			"dropped_session", dropped.SessionID,
		)
	}
	q.items = append(q.items, n)
	q.mu.Unlock()

	// Non-blocking send — if the channel already has a signal, the new
	// item will be picked up when the existing signal is consumed.
	select {
	case q.ch <- struct{}{}:
	default:
	}
}

// Drain removes and returns all pending notifications. Returns nil if empty.
func (q *NotificationQueue) Drain() []Notification {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	items := q.items
	q.items = nil
	return items
}

// Ready returns a channel that receives a value when notifications are
// available. Multiple pushes between drains may produce only one signal —
// always call Drain after receiving to collect all pending items. Spurious
// signals are possible (Drain may return nil); consumers must tolerate this.
func (q *NotificationQueue) Ready() <-chan struct{} {
	return q.ch
}

// Len returns the number of pending notifications.
func (q *NotificationQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
