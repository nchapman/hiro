// Package auth provides session-based authentication for the Hive web UI.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// SessionManager manages in-memory session tokens with expiration.
// Sessions are lost on restart — users simply re-authenticate.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]time.Time // token -> expiry
	ttl      time.Duration
}

// NewSessionManager creates a session manager with the given TTL.
func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
	}
}

// Create generates a new session token and stores it.
func (sm *SessionManager) Create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	sm.mu.Lock()
	sm.sessions[token] = time.Now().Add(sm.ttl)
	sm.mu.Unlock()

	return token, nil
}

// Valid returns true if the token exists and hasn't expired.
func (sm *SessionManager) Valid(token string) bool {
	if token == "" {
		return false
	}
	sm.mu.RLock()
	expiry, ok := sm.sessions[token]
	sm.mu.RUnlock()

	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		sm.Revoke(token)
		return false
	}
	return true
}

// Refresh extends a session's expiry to now + TTL.
func (sm *SessionManager) Refresh(token string) {
	sm.mu.Lock()
	if _, ok := sm.sessions[token]; ok {
		sm.sessions[token] = time.Now().Add(sm.ttl)
	}
	sm.mu.Unlock()
}

// Revoke removes a session token.
func (sm *SessionManager) Revoke(token string) {
	sm.mu.Lock()
	delete(sm.sessions, token)
	sm.mu.Unlock()
}

// Cleanup removes all expired sessions. Call periodically.
func (sm *SessionManager) Cleanup() {
	now := time.Now()
	sm.mu.Lock()
	for token, expiry := range sm.sessions {
		if now.After(expiry) {
			delete(sm.sessions, token)
		}
	}
	sm.mu.Unlock()
}
