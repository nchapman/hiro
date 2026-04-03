package inference

import (
	"log/slog"
	"testing"
	"time"
)

var notifyTestLogger = slog.Default()

func TestNotificationQueue_PushAndDrain(t *testing.T) {
	q := NewNotificationQueue(notifyTestLogger)

	// Empty drain returns nil.
	if items := q.Drain(); items != nil {
		t.Fatalf("expected nil, got %v", items)
	}

	q.Push(Notification{Content: "hello", Source: "test"})
	q.Push(Notification{Content: "world", Source: "test"})

	items := q.Drain()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Content != "hello" || items[1].Content != "world" {
		t.Fatalf("unexpected content: %v", items)
	}

	// Drain again returns nil (queue is empty).
	if items := q.Drain(); items != nil {
		t.Fatalf("expected nil after drain, got %v", items)
	}
}

func TestNotificationQueue_ReadySignal(t *testing.T) {
	q := NewNotificationQueue(notifyTestLogger)

	// Ready channel should not fire on empty queue.
	select {
	case <-q.Ready():
		t.Fatal("ready fired on empty queue")
	default:
	}

	// Push should signal ready.
	q.Push(Notification{Content: "test"})
	select {
	case <-q.Ready():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ready did not fire after push")
	}

	// Drain consumes items.
	items := q.Drain()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	// After drain on empty queue, ready should not fire (tolerate spurious).
	select {
	case <-q.Ready():
		// Spurious signal from prior push — tolerated. Drain returns nil.
		if got := q.Drain(); got != nil {
			t.Fatalf("expected nil on spurious drain, got %v", got)
		}
	default:
		// No signal — expected.
	}

	// Multiple pushes before drain produce at most one signal,
	// but drain gets all items.
	q.Push(Notification{Content: "a"})
	q.Push(Notification{Content: "b"})
	q.Push(Notification{Content: "c"})

	select {
	case <-q.Ready():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ready did not fire")
	}

	items = q.Drain()
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
}

func TestNotificationQueue_Len(t *testing.T) {
	q := NewNotificationQueue(notifyTestLogger)
	if q.Len() != 0 {
		t.Fatalf("expected 0, got %d", q.Len())
	}

	q.Push(Notification{Content: "a"})
	q.Push(Notification{Content: "b"})
	if q.Len() != 2 {
		t.Fatalf("expected 2, got %d", q.Len())
	}

	q.Drain()
	if q.Len() != 0 {
		t.Fatalf("expected 0 after drain, got %d", q.Len())
	}
}

func TestNotificationQueue_MaxDepth(t *testing.T) {
	q := NewNotificationQueue(notifyTestLogger)

	// Fill to capacity.
	for range maxNotifications {
		q.Push(Notification{Content: "msg"})
	}
	if q.Len() != maxNotifications {
		t.Fatalf("expected %d, got %d", maxNotifications, q.Len())
	}

	// One more push should drop the oldest.
	q.Push(Notification{Content: "overflow"})
	if q.Len() != maxNotifications {
		t.Fatalf("expected %d after overflow, got %d", maxNotifications, q.Len())
	}

	items := q.Drain()
	// The first item should be "msg" (second original), last should be "overflow".
	if items[len(items)-1].Content != "overflow" {
		t.Fatalf("expected last item to be overflow, got %q", items[len(items)-1].Content)
	}
}

func TestNotificationQueue_SessionID(t *testing.T) {
	q := NewNotificationQueue(notifyTestLogger)

	q.Push(Notification{Content: "scoped", SessionID: "session-1", Source: "test"})
	q.Push(Notification{Content: "global", Source: "test"})

	items := q.Drain()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].SessionID != "session-1" {
		t.Fatalf("expected session-1, got %q", items[0].SessionID)
	}
	if items[1].SessionID != "" {
		t.Fatalf("expected empty session, got %q", items[1].SessionID)
	}
}

func TestNotificationQueue_ConcurrentPush(t *testing.T) {
	q := NewNotificationQueue(notifyTestLogger)
	done := make(chan struct{})
	n := 100

	for range n {
		go func() {
			q.Push(Notification{Content: "msg"})
			done <- struct{}{}
		}()
	}

	for range n {
		<-done
	}

	items := q.Drain()
	if len(items) != n {
		t.Fatalf("expected %d items, got %d", n, len(items))
	}
}
