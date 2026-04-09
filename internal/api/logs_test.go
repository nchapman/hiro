package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

func TestQueryLogs_Empty(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	req := authedRequest(t, srv, "GET", "/api/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var logs []any
	json.NewDecoder(rec.Body).Decode(&logs)
	if len(logs) != 0 {
		t.Errorf("got %d logs, want 0", len(logs))
	}
}

func TestQueryLogs_WithData(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	pdb.InsertLogs(context.Background(), []platformdb.LogEntry{
		{Level: "INFO", Message: "hello", Component: "api", CreatedAt: time.Now().UTC()},
		{Level: "WARN", Message: "caution", Component: "inference", CreatedAt: time.Now().UTC()},
	})

	req := authedRequest(t, srv, "GET", "/api/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var logs []map[string]any
	json.NewDecoder(rec.Body).Decode(&logs)
	if len(logs) != 2 {
		t.Errorf("got %d logs, want 2", len(logs))
	}
}

func TestQueryLogs_LevelFilter(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	pdb.InsertLogs(context.Background(), []platformdb.LogEntry{
		{Level: "INFO", Message: "info msg", CreatedAt: time.Now().UTC()},
		{Level: "ERROR", Message: "error msg", CreatedAt: time.Now().UTC()},
	})

	req := authedRequest(t, srv, "GET", "/api/logs?level=ERROR", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var logs []map[string]any
	json.NewDecoder(rec.Body).Decode(&logs)
	if len(logs) != 1 {
		t.Errorf("got %d logs, want 1", len(logs))
	}
}

func TestQueryLogs_InvalidLevel(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	req := authedRequest(t, srv, "GET", "/api/logs?level=VERBOSE", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rec.Code)
	}
}

func TestLogSources_Empty(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	req := authedRequest(t, srv, "GET", "/api/logs/sources", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var sources []string
	json.NewDecoder(rec.Body).Decode(&sources)
	if len(sources) != 0 {
		t.Errorf("got %d sources, want 0", len(sources))
	}
}

func TestLogs_StrictAuth_DuringSetup(t *testing.T) {
	// Server with no password set (setup not complete).
	srv, cp := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	// Clear the password to simulate setup-not-complete state.
	cp.SetPasswordHash("")

	// Unauthenticated request to log endpoint should get 401 (auth checked
	// before setup state to avoid leaking setup status).
	req := httptest.NewRequest("GET", "/api/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d during setup, want 401", rec.Code)
	}
}
