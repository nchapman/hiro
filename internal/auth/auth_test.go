package auth

import (
	"testing"
	"time"
)

func TestCreateAndValidate(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	token, err := sm.Create()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if !sm.Valid(token) {
		t.Error("token should be valid")
	}
}

func TestInvalidToken(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	if sm.Valid("nonexistent") {
		t.Error("nonexistent token should be invalid")
	}
	if sm.Valid("") {
		t.Error("empty token should be invalid")
	}
}

func TestRevoke(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	token, _ := sm.Create()
	sm.Revoke(token)
	if sm.Valid(token) {
		t.Error("revoked token should be invalid")
	}
}

func TestExpiration(t *testing.T) {
	sm := NewSessionManager(time.Millisecond)
	token, _ := sm.Create()
	time.Sleep(5 * time.Millisecond)
	if sm.Valid(token) {
		t.Error("expired token should be invalid")
	}
}

func TestCleanup(t *testing.T) {
	sm := NewSessionManager(time.Millisecond)
	sm.Create()
	sm.Create()
	time.Sleep(5 * time.Millisecond)
	sm.Cleanup()

	sm.mu.RLock()
	n := len(sm.sessions)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("cleanup should have removed all sessions, got %d", n)
	}
}

func TestUniqueTokens(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	t1, _ := sm.Create()
	t2, _ := sm.Create()
	if t1 == t2 {
		t.Error("tokens should be unique")
	}
}
