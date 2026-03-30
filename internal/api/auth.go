package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/hivebot/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

// loginLimiter tracks failed login attempts per IP.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

// defaultLimiter is the package-level login rate limiter.
var defaultLimiter = &loginLimiter{attempts: make(map[string][]time.Time)}

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

// tokenSigner returns the cached TokenSigner from the control plane.
func (s *Server) tokenSigner() *auth.TokenSigner {
	if s.cp == nil {
		return nil
	}
	signer, err := s.cp.TokenSigner()
	if err != nil {
		s.logger.Error("failed to get token signer", "error", err)
		return nil
	}
	return signer
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

	ip := clientIP(r)
	if !s.limiter.allow(ip) {
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
		s.limiter.record(ip)
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}

	signer := s.tokenSigner()
	if signer == nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	token := signer.Create()

	http.SetCookie(w, &http.Cookie{
		Name:     "hive_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24 hours
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
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

	// SetPasswordHash also rotates the session secret, invalidating all sessions.
	s.cp.SetPasswordHash(string(newHash))
	if err := s.cp.Save(); err != nil {
		s.logger.Warn("failed to save config after password change", "error", err)
	}

	// Issue a new session token so the requesting user stays logged in
	// after the secret rotation invalidates all existing sessions.
	signer := s.tokenSigner()
	if signer != nil {
		token := signer.Create()
		http.SetCookie(w, &http.Cookie{
			Name:     "hive_session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400,
		})
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// isAuthenticated checks if the request has a valid signed session token.
func (s *Server) isAuthenticated(r *http.Request) bool {
	signer := s.tokenSigner()
	if signer == nil {
		return false
	}

	// Check cookie first
	if cookie, err := r.Cookie("hive_session"); err == nil {
		if signer.Valid(cookie.Value) {
			return true
		}
	}

	// Check Authorization header
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token := strings.TrimPrefix(h, "Bearer ")
		if signer.Valid(token) {
			return true
		}
	}

	return false
}

// clientIP extracts the client's IP address for rate limiting. Proxy
// headers (X-Forwarded-For, X-Real-Ip) are only trusted when the direct
// connection comes from a loopback or private address (i.e., a local
// reverse proxy). This prevents external clients from spoofing their IP
// to bypass the rate limiter. The port is stripped so reconnections from
// different ephemeral ports are correctly grouped.
func clientIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}

	// Only trust proxy headers when the direct peer is a local proxy.
	ip := net.ParseIP(remoteHost)
	trustedProxy := ip != nil && (ip.IsLoopback() || ip.IsPrivate())

	if trustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return remoteHost
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
