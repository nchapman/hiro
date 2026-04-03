// Package loghandler provides a custom slog.Handler that tees log records
// to both a text handler (stdout) and an async SQLite writer, with pub/sub
// support for real-time SSE streaming.
package loghandler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// reservedKeys are slog attribute keys extracted into dedicated DB columns.
// These are matched on the bare key name regardless of group nesting — a
// logger wrapped with WithGroup("foo") that logs "component" will still
// populate the dedicated DB column rather than landing in attrs.
var reservedKeys = map[string]bool{
	"component":   true,
	"instance_id": true,
}

// maxSubscribers is the maximum number of concurrent SSE log subscribers.
const maxSubscribers = 50

// ErrSubscriberLimit is returned when the subscriber cap is reached.
var ErrSubscriberLimit = errors.New("subscriber limit reached")

// Handler implements slog.Handler, teeing records to stdout and SQLite.
type Handler struct {
	text  slog.Handler // stdout delegate
	db    *platformdb.DB
	level slog.Level
	attrs []slog.Attr // pre-resolved attributes from WithAttrs
	group string      // current group prefix

	// Shared state (pointer-shared across WithAttrs/WithGroup clones).
	shared *sharedState
}

type sharedState struct {
	buf  chan platformdb.LogEntry
	subs *subscriberRegistry
	done chan struct{}
	wg   sync.WaitGroup
}

// New creates a Handler that writes to both stdout (via TextHandler) and
// the platform database asynchronously. The handler extracts "component"
// and "instance_id" attributes into dedicated DB columns.
func New(db *platformdb.DB, stdout io.Writer, level slog.Level) *Handler {
	shared := &sharedState{
		buf:  make(chan platformdb.LogEntry, 512),
		subs: newSubscriberRegistry(),
		done: make(chan struct{}),
	}

	h := &Handler{
		text: slog.NewTextHandler(stdout, &slog.HandlerOptions{
			Level: level,
		}),
		db:     db,
		level:  level,
		shared: shared,
	}

	// Start async DB writer goroutine.
	shared.wg.Add(1)
	go h.writeLoop()

	return h
}

// Close drains the async buffer and stops the writer goroutine.
func (h *Handler) Close() {
	close(h.shared.done)
	h.shared.wg.Wait()
}

// Subscribe returns a channel that receives log entries in real-time
// and an unsubscribe function. The channel is buffered; slow consumers
// will have entries dropped. Returns ErrSubscriberLimit if the maximum
// number of subscribers has been reached.
func (h *Handler) Subscribe() (<-chan platformdb.LogEntry, func(), error) {
	return h.shared.subs.subscribe()
}

// Enabled reports whether the handler handles records at the given level.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle processes a log record: writes to stdout synchronously,
// then pushes to the async DB buffer and notifies SSE subscribers.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Always write to stdout first.
	if err := h.text.Handle(ctx, r); err != nil {
		return err
	}

	// Build the DB entry from the record + pre-resolved attrs.
	e := h.buildEntry(r)

	// Fan out to SSE subscribers (non-blocking).
	h.shared.subs.publish(e)

	// Push to async DB writer (non-blocking, drop if full).
	select {
	case h.shared.buf <- e:
	default:
		// Buffer full — drop for DB. SSE subscribers already got it.
	}

	return nil
}

// WithAttrs returns a new Handler with additional pre-resolved attributes.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		text:   h.text.WithAttrs(attrs),
		db:     h.db,
		level:  h.level,
		attrs:  append(cloneAttrs(h.attrs), attrs...),
		group:  h.group,
		shared: h.shared,
	}
}

// WithGroup returns a new Handler with the given group name prepended to keys.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	var groupPrefix string
	if h.group != "" {
		groupPrefix = h.group + name + "."
	} else {
		groupPrefix = name + "."
	}
	return &Handler{
		text:   h.text.WithGroup(name),
		db:     h.db,
		level:  h.level,
		attrs:  cloneAttrs(h.attrs),
		group:  groupPrefix,
		shared: h.shared,
	}
}

// buildEntry extracts a LogEntry from a slog.Record plus pre-resolved attrs.
func (h *Handler) buildEntry(r slog.Record) platformdb.LogEntry {
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	e := platformdb.LogEntry{
		Level:     r.Level.String(),
		Message:   r.Message,
		CreatedAt: ts,
		Attrs:     make(map[string]any),
	}

	// Process pre-resolved attrs from WithAttrs.
	for _, a := range h.attrs {
		if reservedKeys[a.Key] {
			h.setReserved(&e, a.Key, a.Value.String())
		} else {
			e.Attrs[h.group+a.Key] = resolveValue(a.Value)
		}
	}

	// Process record attrs.
	r.Attrs(func(a slog.Attr) bool {
		if reservedKeys[a.Key] {
			h.setReserved(&e, a.Key, a.Value.String())
		} else {
			e.Attrs[h.group+a.Key] = resolveValue(a.Value)
		}
		return true
	})

	if len(e.Attrs) == 0 {
		e.Attrs = nil
	}

	return e
}

func (h *Handler) setReserved(e *platformdb.LogEntry, key, val string) {
	switch key {
	case "component":
		e.Component = val
	case "instance_id":
		e.InstanceID = val
	}
}

// writeLoop batches entries from the buffer and inserts them into the DB.
// It flushes every 100ms or when 64 entries accumulate.
func (h *Handler) writeLoop() {
	defer h.shared.wg.Done()

	const (
		maxBatch  = 64
		flushTick = 100 * time.Millisecond
	)

	ticker := time.NewTicker(flushTick)
	defer ticker.Stop()

	batch := make([]platformdb.LogEntry, 0, maxBatch)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Best-effort insert — don't let DB errors stop the writer.
		_ = h.db.InsertLogs(batch)
		batch = batch[:0]
	}

	for {
		select {
		case e := <-h.shared.buf:
			batch = append(batch, e)
			if len(batch) >= maxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-h.shared.done:
			// Drain remaining entries. Use a short timer to catch
			// in-flight Handle() calls that haven't sent yet.
			drainTimer := time.NewTimer(50 * time.Millisecond)
			for {
				select {
				case e := <-h.shared.buf:
					batch = append(batch, e)
				case <-drainTimer.C:
					drainTimer.Stop()
					flush()
					return
				}
			}
		}
	}
}

// resolveValue converts a slog.Value to a plain Go value for JSON serialization.
func resolveValue(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	case slog.KindGroup:
		m := make(map[string]any)
		for _, a := range v.Group() {
			m[a.Key] = resolveValue(a.Value)
		}
		return m
	default:
		return v.String()
	}
}

func cloneAttrs(attrs []slog.Attr) []slog.Attr {
	if len(attrs) == 0 {
		return nil
	}
	c := make([]slog.Attr, len(attrs))
	copy(c, attrs)
	return c
}

// --- subscriber registry ---

type subscriberRegistry struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
}

type subscriber struct {
	ch chan platformdb.LogEntry
}

func newSubscriberRegistry() *subscriberRegistry {
	return &subscriberRegistry{
		subs: make(map[*subscriber]struct{}),
	}
}

func (r *subscriberRegistry) subscribe() (<-chan platformdb.LogEntry, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.subs) >= maxSubscribers {
		return nil, nil, ErrSubscriberLimit
	}

	s := &subscriber{ch: make(chan platformdb.LogEntry, 64)}
	r.subs[s] = struct{}{}

	unsub := func() {
		r.mu.Lock()
		delete(r.subs, s)
		r.mu.Unlock()
	}
	return s.ch, unsub, nil
}

func (r *subscriberRegistry) publish(e platformdb.LogEntry) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for s := range r.subs {
		select {
		case s.ch <- e:
		default:
			// Slow consumer — drop.
		}
	}
}
