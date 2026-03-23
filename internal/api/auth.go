package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// loginLimiter tracks failed login attempts per IP.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

var limiter = &loginLimiter{attempts: make(map[string][]time.Time)}

// allow returns true if the IP has not exceeded 5 attempts in the last minute.
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	window := now.Add(-time.Minute)

	// Prune old attempts
	attempts := l.attempts[ip]
	valid := attempts[:0]
	for _, t := range attempts {
		if t.After(window) {
			valid = append(valid, t)
		}
	}
	l.attempts[ip] = valid

	return len(valid) < 5
}

// record adds a failed attempt for the given IP.
func (l *loginLimiter) record(ip string) {
	l.mu.Lock()
	l.attempts[ip] = append(l.attempts[ip], time.Now())
	l.mu.Unlock()
}

// authStatusResponse is the response for GET /api/auth/status.
type authStatusResponse struct {
	NeedsSetup    bool `json:"needsSetup"`
	AuthRequired  bool `json:"authRequired"`
	Authenticated bool `json:"authenticated"`
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	needsSetup := s.cp != nil && s.cp.NeedsSetup()
	authRequired := s.cp != nil && !s.cp.NeedsSetup()
	authenticated := !authRequired || s.isAuthenticated(r)

	writeJSON(w, http.StatusOK, authStatusResponse{
		NeedsSetup:    needsSetup,
		AuthRequired:  authRequired,
		Authenticated: authenticated,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	ip := r.RemoteAddr
	if !limiter.allow(ip) {
		http.Error(w, "too many login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	hash := s.cp.PasswordHash()
	if hash == "" {
		http.Error(w, "no password set", http.StatusServiceUnavailable)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		limiter.record(ip)
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}

	token, err := s.sessions.Create()
	if err != nil {
		s.logger.Error("failed to create session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "hive_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(24 * time.Hour / time.Second),
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("hive_session"); err == nil {
		s.sessions.Revoke(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "hive_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.New) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	hash := s.cp.PasswordHash()
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Current)); err != nil {
		http.Error(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		s.logger.Error("failed to hash password", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.cp.SetPasswordHash(string(newHash))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// isAuthenticated checks if the request has a valid session.
// If valid, the session TTL is extended (sliding window).
func (s *Server) isAuthenticated(r *http.Request) bool {
	// Check cookie first
	if cookie, err := r.Cookie("hive_session"); err == nil {
		if s.sessions.Valid(cookie.Value) {
			s.sessions.Refresh(cookie.Value)
			return true
		}
	}

	// Check Authorization header
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		if s.sessions.Valid(token) {
			s.sessions.Refresh(token)
			return true
		}
	}

	return false
}

// requireAuth is middleware that enforces authentication.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Skip auth if no password is set (setup not complete)
		if s.cp == nil || s.cp.NeedsSetup() {
			next(w, r)
			return
		}

		if !s.isAuthenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		next(w, r)
	}
}
