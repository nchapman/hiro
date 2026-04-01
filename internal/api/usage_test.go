package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

func TestTotalUsage(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	req := authedRequest(t, srv, "GET", "/api/usage", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp == nil {
		t.Error("expected non-nil usage response")
	}
}

func TestUsageByModel(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	req := authedRequest(t, srv, "GET", "/api/usage/models", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
}

func TestUsageByDay(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	pdb, _ := platformdb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { pdb.Close() })
	srv.pdb = pdb

	req := authedRequest(t, srv, "GET", "/api/usage/daily?limit=7", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
}

func TestUsage_NoDB(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	// No pdb set — should return 503.

	req := authedRequest(t, srv, "GET", "/api/usage", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503 when DB is nil", rec.Code)
	}
}

func TestUsage_RequiresAuth(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	req := httptest.NewRequest("GET", "/api/usage", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 for unauthenticated usage", rec.Code)
	}
}
