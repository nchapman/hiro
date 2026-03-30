package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nchapman/hivebot/internal/controlplane"
)

// newSetupServer creates a server that needs setup (no password set).
func newSetupServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp, err := controlplane.Load(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(logger, nil, cp, nil, "")
	srv.limiter = &loginLimiter{attempts: make(map[string][]time.Time)}
	return srv
}

func TestSetup_Success(t *testing.T) {
	srv := newSetupServer(t)

	body, _ := json.Marshal(map[string]string{
		"password":      "testpass123",
		"provider_type": "anthropic",
		"api_key":       "sk-test-key-12345",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Should set a session cookie.
	var foundCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "hive_session" {
			foundCookie = true
		}
	}
	if !foundCookie {
		t.Error("expected hive_session cookie")
	}

	// Setup should no longer be needed.
	if srv.cp.NeedsSetup() {
		t.Error("expected NeedsSetup=false after setup")
	}

	// Provider should be configured.
	if !srv.cp.IsConfigured() {
		t.Error("expected provider to be configured")
	}
}

func TestSetup_ShortPassword(t *testing.T) {
	srv := newSetupServer(t)

	body, _ := json.Marshal(map[string]string{
		"password":      "short",
		"provider_type": "anthropic",
		"api_key":       "sk-test-key",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for short password, got %d", rec.Code)
	}
}

func TestSetup_MissingFields(t *testing.T) {
	srv := newSetupServer(t)

	body, _ := json.Marshal(map[string]string{
		"password": "longpassword",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing fields, got %d", rec.Code)
	}
}

func TestSetup_AlreadyComplete(t *testing.T) {
	srv, _ := newAuthTestServer(t) // password already set

	body, _ := json.Marshal(map[string]string{
		"password":      "testpass123",
		"provider_type": "anthropic",
		"api_key":       "sk-test-key",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for already-setup, got %d", rec.Code)
	}
}

func TestSetup_CrossOriginBlocked(t *testing.T) {
	srv := newSetupServer(t)

	body, _ := json.Marshal(map[string]string{
		"password":      "testpass123",
		"provider_type": "anthropic",
		"api_key":       "sk-test-key",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.com")
	req.Host = "localhost:8080"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-origin, got %d", rec.Code)
	}
}
