package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestWatchFileWrite(t *testing.T) {
	dir := t.TempDir()

	// Create a file before starting the watcher.
	initial := filepath.Join(dir, "test.txt")
	os.WriteFile(initial, []byte("hello"), 0o644)

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var got []Event
	var mu sync.Mutex
	done := make(chan struct{})

	w.Subscribe("test.txt", func(events []Event) {
		mu.Lock()
		got = append(got, events...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Modify the file.
	os.WriteFile(initial, []byte("world"), 0o644)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one event")
	}
	if got[0].Path != "test.txt" {
		t.Errorf("got path %q, want %q", got[0].Path, "test.txt")
	}
}

func TestWatchFileCreate(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var got []Event
	var mu sync.Mutex
	done := make(chan struct{})

	w.Subscribe("**/*.md", func(events []Event) {
		mu.Lock()
		got = append(got, events...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Create a new file.
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Hello"), 0o644)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one event")
	}
	if got[0].Path != "readme.md" {
		t.Errorf("got path %q, want %q", got[0].Path, "readme.md")
	}
}

func TestWatchSubdirectory(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a subdirectory.
	os.MkdirAll(filepath.Join(dir, "agents", "test"), 0o755)

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var got []Event
	var mu sync.Mutex
	done := make(chan struct{})

	w.Subscribe("agents/**/*.md", func(events []Event) {
		mu.Lock()
		got = append(got, events...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Create a file in a subdirectory.
	os.WriteFile(filepath.Join(dir, "agents", "test", "agent.md"), []byte("---\nname: test\n---"), 0o644)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one event")
	}
	if got[0].Path != "agents/test/agent.md" {
		t.Errorf("got path %q, want %q", got[0].Path, "agents/test/agent.md")
	}
}

func TestWatchNewDirectory(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var got []Event
	var mu sync.Mutex
	done := make(chan struct{})

	w.Subscribe("newdir/**", func(events []Event) {
		mu.Lock()
		got = append(got, events...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Create a new directory and a file inside it atomically —
	// synthesizeExisting should catch the file even without a delay.
	os.MkdirAll(filepath.Join(dir, "newdir"), 0o755)
	os.WriteFile(filepath.Join(dir, "newdir", "file.txt"), []byte("content"), 0o644)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one event")
	}
}

func TestWatchPatternNoMatch(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var called atomic.Bool
	w.Subscribe("*.go", func(events []Event) {
		called.Store(true)
	})

	// Create a .txt file — should not match *.go pattern.
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0o644)
	// Negative assertion: verify callback does NOT fire. A short sleep is
	// the correct approach here since there's no event to wait for.
	time.Sleep(200 * time.Millisecond)

	if called.Load() {
		t.Error("handler should not have been called for non-matching pattern")
	}
}

func TestWatchUnsubscribe(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var called atomic.Bool
	unsub := w.Subscribe("**", func(events []Event) {
		called.Store(true)
	})
	unsub()

	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0o644)
	// Negative assertion: verify callback does NOT fire after unsubscribe.
	time.Sleep(200 * time.Millisecond)

	if called.Load() {
		t.Error("handler should not have been called after unsubscribe")
	}
}

func TestWatchDebounce(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "rapid.txt")
	os.WriteFile(f, []byte("v0"), 0o644)

	w, err := New(dir, testLogger(), WithDebounce(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var callCount int
	var mu sync.Mutex
	done := make(chan struct{}, 10)

	w.Subscribe("rapid.txt", func(events []Event) {
		mu.Lock()
		callCount++
		mu.Unlock()
		done <- struct{}{}
	})

	// Rapid writes — should be coalesced into one batch.
	for i := range 10 {
		os.WriteFile(f, []byte("v"+string(rune('1'+i))), 0o644)
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Give a bit more time to see if extra dispatches happen.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	// Debouncing should coalesce rapid writes into very few dispatches (ideally 1).
	if callCount > 3 {
		t.Errorf("expected debouncing to coalesce writes, got %d dispatches", callCount)
	}
}

func TestWatchMultipleSubscribers(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var mu sync.Mutex
	var sub1Got, sub2Got []Event
	done1 := make(chan struct{}, 1)
	done2 := make(chan struct{}, 1)

	w.Subscribe("*.txt", func(events []Event) {
		mu.Lock()
		sub1Got = append(sub1Got, events...)
		mu.Unlock()
		select {
		case done1 <- struct{}{}:
		default:
		}
	})
	w.Subscribe("**", func(events []Event) {
		mu.Lock()
		sub2Got = append(sub2Got, events...)
		mu.Unlock()
		select {
		case done2 <- struct{}{}:
		default:
		}
	})

	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0o644)

	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sub1")
	}
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sub2")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sub1Got) == 0 {
		t.Error("subscriber 1 should have received events")
	}
	if len(sub2Got) == 0 {
		t.Error("subscriber 2 should have received events")
	}
}

func TestWatchSkipHiddenFiles(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, testLogger(), WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var called atomic.Bool
	w.Subscribe("**", func(events []Event) {
		called.Store(true)
	})

	// Create a hidden file.
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("secret"), 0o644)
	// Negative assertion: verify callback does NOT fire for hidden files.
	time.Sleep(200 * time.Millisecond)

	if called.Load() {
		t.Error("handler should not have been called for hidden files")
	}
}

func TestOpString(t *testing.T) {
	tests := []struct {
		op   Op
		want string
	}{
		{Create, "create"},
		{Write, "write"},
		{Remove, "remove"},
		{Rename, "rename"},
		{Op(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.op.String(); got != tt.want {
			t.Errorf("Op(%d).String() = %q, want %q", tt.op, got, tt.want)
		}
	}
}
