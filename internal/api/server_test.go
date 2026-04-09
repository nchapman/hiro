package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

func newTestServer() *Server {
	logger := slog.Default()
	return NewServer(logger, nil, nil, nil, "")
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestUnmatchedAPIReturns404(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleClearInstance(t *testing.T) {
	srv, mgr, agentName, token := newInstanceTestServer(t)

	id, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	req := withAuth(httptest.NewRequest("POST", "/api/instances/"+id+"/clear", nil), token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["new_session_id"] == "" {
		t.Error("expected non-empty new_session_id in response")
	}
}

func TestHandleClearInstance_WithChannel(t *testing.T) {
	srv, mgr, agentName, token := newInstanceTestServer(t)

	id, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	req := withAuth(httptest.NewRequest("POST", "/api/instances/"+id+"/clear?channel=web", nil), token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["new_session_id"] == "" {
		t.Error("expected non-empty new_session_id in response")
	}
}

func TestHandleClearInstance_NotFound(t *testing.T) {
	srv, _, _, token := newInstanceTestServer(t)

	req := withAuth(httptest.NewRequest("POST", "/api/instances/nonexistent/clear", nil), token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestHandleSessionMessages_NotFound(t *testing.T) {
	srv, _, _, token := newInstanceTestServer(t)

	req := withAuth(httptest.NewRequest("GET", "/api/sessions/nonexistent/messages", nil), token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestHandleInstanceUsage_SessionOwnership(t *testing.T) {
	srv, mgr, agentName, token := newInstanceTestServer(t)

	// Create two instances — inst2 is a child of inst1 so both have DB rows.
	inst1ID, err := mgr.CreateInstance(context.Background(), agentName, "", "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance inst1: %v", err)
	}
	inst2ID, err := mgr.CreateInstance(context.Background(), agentName, inst1ID, "persistent", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateInstance inst2: %v", err)
	}

	// srv.pdb is already set by newInstanceTestServer — use it directly to
	// insert a session that belongs to inst2.
	otherSession := platformdb.Session{
		ID:         "session-for-inst2",
		InstanceID: inst2ID,
		AgentName:  agentName,
		Mode:       "persistent",
		Status:     "running",
	}
	if err := srv.pdb.CreateSession(context.Background(), otherSession); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Query inst1's usage with a session_id that belongs to inst2 — should be 404.
	req := withAuth(httptest.NewRequest("GET", "/api/instances/"+inst1ID+"/usage?session_id=session-for-inst2", nil), token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for session belonging to different instance", rec.Code)
	}
}
