package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

const (
	// sessionCookieMaxAge is 24 hours in seconds.
	sessionCookieMaxAge = 86400

	// maxLoginAttemptsPerMinute is the rate limit for failed login attempts per IP.
	maxLoginAttemptsPerMinute = 5
)

// sessionCookieName returns a port-scoped cookie name so multiple instances
// on the same host (e.g., localhost:8080 and localhost:8082) don't clobber
// each other's sessions.
func sessionCookieName(r *http.Request) string {
	_, port, err := net.SplitHostPort(r.Host)
	if err != nil || port == "" || port == "80" || port == "443" {
		return sessionCookieBase
	}
	return sessionCookieBase + "_" + port
}

const sessionCookieBase = "hiro_session"

// setSessionCookie writes a session token cookie to the response.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(r),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   sessionCookieMaxAge,
	})
}

// clearSessionCookie expires the session cookie.
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(r),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

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

	return len(valid) < maxLoginAttemptsPerMinute
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
	NeedsSetup    bool   `json:"needsSetup"`
	AuthRequired  bool   `json:"authRequired"`
	Authenticated bool   `json:"authenticated"`
	Mode          string `json:"mode,omitempty"` // "standalone", "leader", or "worker"
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	needsSetup := s.cp != nil && s.cp.NeedsSetup()
	authRequired := s.cp != nil && !s.cp.NeedsSetup()
	authenticated := !authRequired || s.isAuthenticated(r)

	var mode string
	if s.cp != nil {
		mode = s.cp.ClusterMode()
	}

	writeJSON(w, http.StatusOK, authStatusResponse{
		NeedsSetup:    needsSetup,
		AuthRequired:  authRequired,
		Authenticated: authenticated,
		Mode:          mode,
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

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
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
		s.logger.Warn("login failed", "reason", "invalid_password")
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}

	signer := s.tokenSigner()
	if signer == nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	token := signer.Create()
	setSessionCookie(w, r, token)

	s.logger.Info("login succeeded")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.cp == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.New) < minPasswordLength {
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
		setSessionCookie(w, r, token)
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
	if cookie, err := r.Cookie(sessionCookieName(r)); err == nil {
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
// During setup (no password set), requests are passed through unauthenticated
// so the setup UI can function.
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

// requireStrictAuth is middleware that enforces authentication even during setup.
// Use this for endpoints that should never be accessible without authentication
// (e.g., logs which may contain sensitive operational data).
func (s *Server) requireStrictAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cp == nil || s.cp.NeedsSetup() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "setup not complete"})
			return
		}

		if !s.isAuthenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		next(w, r)
	}
}
