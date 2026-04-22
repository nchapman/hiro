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

	"github.com/nchapman/hiro/internal/controlplane"
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
		"mode":          "standalone",
		"provider_type": "anthropic",
		"api_key":       "sk-test-key-12345",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Should set a session cookie.
	var foundCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "hiro_session" {
			foundCookie = true
		}
	}
	if !foundCookie {
		t.Error("expected hiro_session cookie")
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

func TestSetup_Leader(t *testing.T) {
	srv := newSetupServer(t)

	body, _ := json.Marshal(map[string]string{
		"password":      "testpass123",
		"mode":          "leader",
		"provider_type": "anthropic",
		"api_key":       "sk-test-key-12345",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	swarmCode, ok := resp["swarm_code"].(string)
	if !ok || swarmCode == "" {
		t.Errorf("expected non-empty swarm_code in response, got: %v", resp)
	}
	t.Logf("response: %v", resp)

	// Cluster config should be set.
	if srv.cp.ClusterSwarmCode() != swarmCode {
		t.Errorf("swarm code mismatch: config=%q response=%q", srv.cp.ClusterSwarmCode(), swarmCode)
	}
	if srv.cp.ClusterMode() != "leader" {
		t.Errorf("expected mode=leader, got %q", srv.cp.ClusterMode())
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
	req.RemoteAddr = "127.0.0.1:1234"
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
		"mode":     "standalone",
	})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
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
		"mode":          "standalone",
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

