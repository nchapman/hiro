// Package watcher provides a recursive filesystem watcher for HIVE_ROOT.
// Components subscribe with glob patterns and receive debounced change events.
package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
)

// Op describes the type of filesystem change.
type Op int

const (
	Create Op = iota
	Write
	Remove
	Rename
)

func (o Op) String() string {
	switch o {
	case Create:
		return "create"
	case Write:
		return "write"
	case Remove:
		return "remove"
	case Rename:
		return "rename"
	default:
		return "unknown"
	}
}

// Event represents a debounced filesystem change.
type Event struct {
	// Path is relative to the watcher root (e.g. "agents/coordinator/agent.md").
	Path string
	Op   Op
}

// Handler is called when a matching filesystem event occurs.
// It receives a batch of debounced events.
type Handler func(events []Event)

type subscription struct {
	pattern string  // glob pattern relative to root (e.g. "config.yaml", "agents/**/*.md")
	handler Handler
}

// Watcher watches a directory tree and dispatches debounced events to subscribers.
type Watcher struct {
	root     string
	fsw      *fsnotify.Watcher
	logger   *slog.Logger
	debounce time.Duration

	mu     sync.RWMutex
	subs   map[uint64]subscription
	nextID uint64

	done chan struct{}
}

// Option configures the Watcher.
type Option func(*Watcher)

// WithDebounce sets the debounce duration. Default is 100ms.
func WithDebounce(d time.Duration) Option {
	return func(w *Watcher) { w.debounce = d }
}

// New creates a Watcher that recursively watches root for filesystem changes.
// Call Close to stop watching and release resources.
func New(root string, logger *slog.Logger, opts ...Option) (*Watcher, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		root:     absRoot,
		fsw:      fsw,
		logger:   logger,
		debounce: 100 * time.Millisecond,
		subs:     make(map[uint64]subscription),
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		opt(w)
	}

	// Walk the tree and add all directories.
	if err := w.addRecursive(absRoot); err != nil {
		fsw.Close()
		return nil, err
	}

	go w.loop()
	return w, nil
}

// Subscribe registers a handler that will be called for events matching
// the given glob pattern. The pattern is matched against the path relative
// to the watcher root using doublestar syntax (e.g. "**/*.md", "config.yaml",
// "agents/*/agent.md"). Returns an unsubscribe function.
//
// Note: a dispatch already in progress may complete after unsubscribe returns.
// No new dispatches will be started for the handler after unsubscribe.
func (w *Watcher) Subscribe(pattern string, handler Handler) func() {
	w.mu.Lock()
	id := w.nextID
	w.nextID++
	w.subs[id] = subscription{pattern: pattern, handler: handler}
	w.mu.Unlock()

	return func() {
		w.mu.Lock()
		delete(w.subs, id)
		w.mu.Unlock()
	}
}

// Close stops the watcher and releases all resources.
func (w *Watcher) Close() error {
	err := w.fsw.Close()
	<-w.done // wait for loop to exit
	return err
}

func (w *Watcher) loop() {
	defer close(w.done)

	// pending accumulates events during the debounce window.
	pending := make(map[string]Op)
	var timer *time.Timer
	var timerC <-chan time.Time

	flush := func() {
		if len(pending) == 0 {
			return
		}
		events := make([]Event, 0, len(pending))
		for path, op := range pending {
			events = append(events, Event{Path: path, Op: op})
		}
		pending = make(map[string]Op)
		w.dispatch(events)
	}

	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				flush()
				return
			}
			w.handleRawEvent(ev, pending)

			// Reset debounce timer.
			if timer == nil {
				timer = time.NewTimer(w.debounce)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(w.debounce)
			}

		case <-timerC:
			flush()
			timer = nil
			timerC = nil

		case err, ok := <-w.fsw.Errors:
			if !ok {
				flush()
				return
			}
			w.logger.Warn("filesystem watcher error", "error", err)
		}
	}
}

// skipDir returns true for directories we should never watch.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "__pycache__":
		return true
	}
	return false
}

func (w *Watcher) handleRawEvent(ev fsnotify.Event, pending map[string]Op) {
	rel, err := filepath.Rel(w.root, ev.Name)
	if err != nil {
		return
	}
	// Normalize to forward slashes for consistent matching.
	rel = filepath.ToSlash(rel)

	// Skip hidden files/dirs at the top level (like .env, .git).
	if strings.HasPrefix(rel, ".") {
		return
	}

	op := toOp(ev.Op)

	// If a directory was created, start watching it recursively and
	// synthesize Create events for any files already present (they may
	// have been written before the watch was established).
	if ev.Op.Has(fsnotify.Create) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if !skipDir(info.Name()) {
				w.addRecursive(ev.Name)
				w.synthesizeExisting(ev.Name, pending)
			}
			return // don't dispatch events for directory creation itself
		}
	}

	// Skip directory events (we only care about file content changes).
	if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
		return
	}

	// For removes, the latest op wins. For writes after create, keep create.
	if existing, ok := pending[rel]; ok {
		if existing == Create && op == Write {
			return // already marked as create
		}
	}
	pending[rel] = op
}

func (w *Watcher) dispatch(events []Event) {
	w.mu.RLock()
	subs := make(map[uint64]subscription, len(w.subs))
	for id, sub := range w.subs {
		subs[id] = sub
	}
	w.mu.RUnlock()

	for _, sub := range subs {
		var matched []Event
		for _, ev := range events {
			ok, err := doublestar.Match(sub.pattern, ev.Path)
			if err != nil {
				w.logger.Warn("bad watcher pattern", "pattern", sub.pattern, "error", err)
				continue
			}
			if ok {
				matched = append(matched, ev)
			}
		}
		if len(matched) > 0 {
			w.safeCall(sub.handler, matched)
		}
	}
}

func (w *Watcher) safeCall(h Handler, events []Event) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("watcher handler panicked", "panic", r)
		}
	}()
	h(events)
}

// synthesizeExisting reads a newly watched directory and injects Create events
// for any files already present, covering the race between mkdir and watch add.
func (w *Watcher) synthesizeExisting(dir string, pending map[string]Op) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		rel, err := filepath.Rel(w.root, filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if _, ok := pending[rel]; !ok {
			pending[rel] = Create
		}
	}
}

func (w *Watcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible dirs
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			// Skip hidden directories (except root itself).
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if err := w.fsw.Add(path); err != nil {
				w.logger.Debug("failed to watch directory", "path", path, "error", err)
			}
		}
		return nil
	})
}

func toOp(op fsnotify.Op) Op {
	switch {
	case op.Has(fsnotify.Create):
		return Create
	case op.Has(fsnotify.Remove):
		return Remove
	case op.Has(fsnotify.Rename):
		return Rename
	default:
		return Write
	}
}
