package controlplane

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/nchapman/hiro/internal/auth"
)

const sessionTTL = 24 * time.Hour

// NeedsSetup returns true if no admin password has been set (first run).
func (cp *ControlPlane) NeedsSetup() bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Auth.PasswordHash == ""
}

// PasswordHash returns the bcrypt password hash.
func (cp *ControlPlane) PasswordHash() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Auth.PasswordHash
}

// SetPasswordHash stores the bcrypt password hash and rotates the session
// secret, invalidating all existing sessions.
func (cp *ControlPlane) SetPasswordHash(hash string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.config.Auth.PasswordHash = hash
	// Rotate session secret to invalidate existing sessions.
	cp.config.Auth.SessionSecret = ""
	cp.signer = nil
}

// TokenSigner returns a cached HMAC token signer for session authentication.
// On first call, it generates a signing secret and persists it to config.yaml
// so sessions survive restarts. The signer is invalidated when the password
// changes (which rotates the secret).
func (cp *ControlPlane) TokenSigner() (*auth.TokenSigner, error) {
	// Fast path: read lock for the common case where signer is cached.
	cp.mu.RLock()
	if s := cp.signer; s != nil {
		cp.mu.RUnlock()
		return s, nil
	}
	cp.mu.RUnlock()

	// Slow path: write lock to generate and cache a new signer.
	cp.mu.Lock()
	defer cp.mu.Unlock()

	// Re-check after acquiring write lock.
	if cp.signer != nil {
		return cp.signer, nil
	}

	var secret []byte
	if cp.config.Auth.SessionSecret != "" {
		var err error
		secret, err = hex.DecodeString(cp.config.Auth.SessionSecret)
		if err != nil {
			return nil, fmt.Errorf("decoding session secret: %w", err)
		}
	} else {
		var err error
		secret, err = auth.GenerateSecret()
		if err != nil {
			return nil, fmt.Errorf("generating session secret: %w", err)
		}
		cp.config.Auth.SessionSecret = hex.EncodeToString(secret)

		// Persist immediately so the secret survives crashes.
		if err := cp.saveUnlocked(); err != nil {
			cp.config.Auth.SessionSecret = "" // roll back
			return nil, fmt.Errorf("persisting session secret: %w", err)
		}
	}

	signer, err := auth.NewTokenSigner(secret, sessionTTL)
	if err != nil {
		return nil, fmt.Errorf("creating token signer: %w", err)
	}
	cp.signer = signer
	return cp.signer, nil
}
