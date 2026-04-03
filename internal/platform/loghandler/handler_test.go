package loghandler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

func openTestDB(t *testing.T) *platformdb.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := platformdb.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestHandler_WritesToDB(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelInfo)
	logger := slog.New(h)

	logger.Info("test message", "key", "value")

	// Close flushes the async buffer.
	h.Close()

	logs, err := db.QueryLogs(context.Background(), platformdb.LogQuery{})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %d logs, want 1", len(logs))
	}
	if logs[0].Message != "test message" {
		t.Errorf("message = %q, want 'test message'", logs[0].Message)
	}
	if logs[0].Level != "INFO" {
		t.Errorf("level = %q, want INFO", logs[0].Level)
	}
	if logs[0].Attrs["key"] != "value" {
		t.Errorf("attrs[key] = %v, want 'value'", logs[0].Attrs["key"])
	}
}

func TestHandler_ExtractsReservedKeys(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelInfo)
	logger := slog.New(h).With("component", "api", "instance_id", "inst-1")

	logger.Info("request handled")
	h.Close()

	logs, _ := db.QueryLogs(context.Background(), platformdb.LogQuery{})
	if len(logs) != 1 {
		t.Fatalf("got %d logs, want 1", len(logs))
	}
	if logs[0].Component != "api" {
		t.Errorf("component = %q, want 'api'", logs[0].Component)
	}
	if logs[0].InstanceID != "inst-1" {
		t.Errorf("instance_id = %q, want 'inst-1'", logs[0].InstanceID)
	}
	// Reserved keys should not appear in attrs.
	if logs[0].Attrs != nil {
		t.Errorf("attrs should be nil (reserved keys extracted), got %v", logs[0].Attrs)
	}
}

func TestHandler_WithGroup(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelInfo)
	logger := slog.New(h).WithGroup("req")

	logger.Info("handled", "method", "GET")
	h.Close()

	logs, _ := db.QueryLogs(context.Background(), platformdb.LogQuery{})
	if len(logs) != 1 {
		t.Fatalf("got %d logs", len(logs))
	}
	// Grouped attrs should have the prefix.
	if logs[0].Attrs["req.method"] != "GET" {
		t.Errorf("attrs = %v, want req.method=GET", logs[0].Attrs)
	}
}

func TestHandler_ReservedKeysUnderGroup(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelInfo)
	// component set at top level, then grouped attrs added.
	logger := slog.New(h).With("component", "inference").WithGroup("detail")

	logger.Info("step", "tool", "bash")
	h.Close()

	logs, _ := db.QueryLogs(context.Background(), platformdb.LogQuery{})
	if len(logs) != 1 {
		t.Fatalf("got %d logs", len(logs))
	}
	if logs[0].Component != "inference" {
		t.Errorf("component = %q, want 'inference'", logs[0].Component)
	}
	if logs[0].Attrs["detail.tool"] != "bash" {
		t.Errorf("attrs = %v, want detail.tool=bash", logs[0].Attrs)
	}
}

func TestHandler_LevelFiltering(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelWarn)
	logger := slog.New(h)

	logger.Info("should be dropped")
	logger.Warn("should be kept")
	h.Close()

	logs, _ := db.QueryLogs(context.Background(), platformdb.LogQuery{})
	if len(logs) != 1 {
		t.Fatalf("got %d logs, want 1 (info should be filtered)", len(logs))
	}
	if logs[0].Message != "should be kept" {
		t.Errorf("message = %q", logs[0].Message)
	}
}

func TestHandler_SubscriberReceivesEntries(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelInfo)
	logger := slog.New(h)

	ch, unsub, err := h.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsub()

	logger.Info("streamed")

	select {
	case entry := <-ch:
		if entry.Message != "streamed" {
			t.Errorf("subscriber got %q, want 'streamed'", entry.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive entry within 1s")
	}

	h.Close()
}

func TestHandler_SubscriberLimit(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelInfo)
	defer h.Close()

	var unsubs []func()
	for i := range maxSubscribers {
		_, unsub, err := h.Subscribe()
		if err != nil {
			t.Fatalf("Subscribe %d: %v", i, err)
		}
		unsubs = append(unsubs, unsub)
	}

	// Next subscribe should fail.
	_, _, err := h.Subscribe()
	if !errors.Is(err, ErrSubscriberLimit) {
		t.Errorf("expected ErrSubscriberLimit, got %v", err)
	}

	// Unsubscribe one, then subscribe should work again.
	unsubs[0]()
	_, unsub, err := h.Subscribe()
	if err != nil {
		t.Errorf("subscribe after unsub: %v", err)
	}
	unsub()

	for _, u := range unsubs[1:] {
		u()
	}
}

func TestHandler_BatchedWrites(t *testing.T) {
	db := openTestDB(t)
	h := New(db, io.Discard, slog.LevelInfo)
	logger := slog.New(h)

	// Write enough entries to trigger a batch flush.
	for range 100 {
		logger.Info("batch entry")
	}

	h.Close()

	logs, _ := db.QueryLogs(context.Background(), platformdb.LogQuery{Limit: 200})
	if len(logs) != 100 {
		t.Errorf("got %d logs, want 100", len(logs))
	}
}
