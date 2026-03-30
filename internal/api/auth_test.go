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
	"golang.org/x/crypto/bcrypt"
)

// newAuthTestServer creates a Server with a real ControlPlane backed by a temp config file.
// The password is set to "testpass1" and the server is ready for authenticated requests.
func newAuthTestServer(t *testing.T) (*Server, *controlplane.ControlPlane) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp, err := controlplane.Load(path, logger)
	if err != nil {
		t.Fatalf("controlplane.Load: %v", err)
	}

	// Set password so auth is required.
	hash, _ := bcrypt.GenerateFromPassword([]byte("testpass1"), bcrypt.MinCost)
	cp.SetPasswordHash(string(hash))
	cp.Save()

	srv := NewServer(logger, nil)
	srv.cp = cp
	srv.limiter = &loginLimiter{attempts: make(map[string][]time.Time)}
	return srv, cp
}

// loginAndGetToken performs a login and returns the session cookie value.
func loginAndGetToken(t *testing.T, srv *Server) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": "testpass1"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "hive_session" {
			return c.Value
		}
	}
	t.Fatal("no hive_session cookie in login response")
	return ""
}

// authedRequest creates a request with a valid session cookie.
func authedRequest(t *testing.T, srv *Server, method, path string, body []byte) *http.Request {
	t.Helper()
	token := loginAndGetToken(t, srv)
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.AddCookie(&http.Cookie{Name: "hive_session", Value: token})
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestAuthStatus_NeedsSetup(t *testing.T) {
	// No password set → needsSetup=true.
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp, _ := controlplane.Load(filepath.Join(dir, "config.yaml"), logger)

	srv := NewServer(logger, nil)
	srv.cp = cp

	req := httptest.NewRequest("GET", "/api/auth/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var resp authStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.NeedsSetup {
		t.Error("expected needsSetup=true when no password set")
	}
	if resp.AuthRequired {
		t.Error("expected authRequired=false during setup")
	}
}

func TestAuthStatus_AuthRequired(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	req := httptest.NewRequest("GET", "/api/auth/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var resp authStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.NeedsSetup {
		t.Error("expected needsSetup=false when password is set")
	}
	if !resp.AuthRequired {
		t.Error("expected authRequired=true when password is set")
	}
	if resp.Authenticated {
		t.Error("expected authenticated=false without token")
	}
}

func TestLogin_Success(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	body, _ := json.Marshal(map[string]string{"password": "testpass1"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	// Verify cookie is set.
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "hive_session" && c.Value != "" {
			found = true
			if !c.HttpOnly {
				t.Error("cookie should be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("no hive_session cookie in response")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	body, _ := json.Marshal(map[string]string{"password": "wrongpass"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestLogin_RateLimiter(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	body, _ := json.Marshal(map[string]string{"password": "wrong"})

	// 5 attempts should succeed (even if password is wrong).
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("rate limited on attempt %d, expected 5 attempts allowed", i+1)
		}
	}

	// 6th attempt should be rate limited.
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429 on 6th attempt", rec.Code)
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	req := authedRequest(t, srv, "POST", "/api/auth/logout", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	for _, c := range rec.Result().Cookies() {
		if c.Name == "hive_session" && c.MaxAge != -1 {
			t.Error("expected MaxAge=-1 to clear cookie")
		}
	}
}

func TestRequireAuth_RejectsUnauthenticated(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	req := httptest.NewRequest("GET", "/api/instances", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 for unauthenticated request", rec.Code)
	}
}

func TestRequireAuth_AcceptsBearerToken(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	token := loginAndGetToken(t, srv)

	req := httptest.NewRequest("GET", "/api/instances", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Should not be 401 (may be 200 or other, but not auth failure).
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("Bearer token auth rejected")
	}
}

func TestRequireAuth_SkipsDuringSetup(t *testing.T) {
	// No password set → requireAuth should pass through.
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp, _ := controlplane.Load(filepath.Join(dir, "config.yaml"), logger)

	srv := NewServer(logger, nil)
	srv.cp = cp

	req := httptest.NewRequest("GET", "/api/instances", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// Should not be 401 — auth is skipped during setup.
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("auth should be skipped when setup is incomplete")
	}
}

func TestChangePassword_Success(t *testing.T) {
	srv, cp := newAuthTestServer(t)
	token := loginAndGetToken(t, srv)

	body, _ := json.Marshal(map[string]string{
		"current": "testpass1",
		"new":     "newpass12",
	})
	req := httptest.NewRequest("POST", "/api/auth/password", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "hive_session", Value: token})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200. body=%s", rec.Code, rec.Body.String())
	}

	// Old token should be invalidated (password change rotates session secret).
	req2 := httptest.NewRequest("GET", "/api/instances", nil)
	req2.AddCookie(&http.Cookie{Name: "hive_session", Value: token})
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Error("old session token should be invalidated after password change")
	}

	// New password should work.
	body3, _ := json.Marshal(map[string]string{"password": "newpass12"})
	req3 := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	srv.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("new password login failed: status=%d body=%s", rec3.Code, rec3.Body.String())
	}
	_ = cp
}

func TestChangePassword_TooShort(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	body, _ := json.Marshal(map[string]string{
		"current": "testpass1",
		"new":     "short",
	})
	req := authedRequest(t, srv, "POST", "/api/auth/password", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for short password", rec.Code)
	}
}

func TestChangePassword_WrongCurrent(t *testing.T) {
	srv, _ := newAuthTestServer(t)

	body, _ := json.Marshal(map[string]string{
		"current": "wrongpass",
		"new":     "newpass12",
	})
	req := authedRequest(t, srv, "POST", "/api/auth/password", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 for wrong current password", rec.Code)
	}
}
