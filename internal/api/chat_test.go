package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveChatInstance_NoManager(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws/chat", nil)
	_, errStr := s.resolveChatInstance(req)
	if errStr == "" {
		t.Fatal("expected error when no manager set")
	}
	if !strings.Contains(errStr, "no agent configured") {
		t.Fatalf("unexpected error: %q", errStr)
	}
}

func TestResolveChatInstance_WithManager(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	// Set a non-nil manager (we just need the field set, not a real manager).
	// Since manager is *agent.Manager, we can't easily create a fake one,
	// but we can set leaderID and check the flow.
	s.leaderID = "leader-123"
	// manager is still nil, so hasManager() returns false.
	req := httptest.NewRequest("GET", "/ws/chat", nil)
	_, errStr := s.resolveChatInstance(req)
	if errStr == "" {
		t.Fatal("expected error when manager is nil")
	}
}

func TestResolveChatInstance_WithInstanceIDParam(t *testing.T) {
	t.Parallel()

	// We cannot easily set a real manager, but we can test the query param
	// extraction logic by verifying the URL parsing path. When manager is nil,
	// we get the "no agent configured" error before reaching the param logic.
	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws/chat?instance_id=custom-123", nil)
	_, errStr := s.resolveChatInstance(req)
	// Still errors because no manager, but that's expected.
	if errStr == "" {
		t.Fatal("expected error when manager is nil")
	}
}

func TestHandleChat_NoManager_Returns503(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws/chat", nil)
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleChat_AuthRequired_NoCookie_Returns401(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	req := httptest.NewRequest("GET", "/ws/chat", nil)
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
