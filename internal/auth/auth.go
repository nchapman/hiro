// Package auth provides HMAC-signed token authentication for the Hiro web UI.
// Tokens are stateless — the server only needs a persistent signing secret
// (stored in config.yaml) to validate them across restarts.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

// TokenSigner creates and validates HMAC-signed session tokens.
// Tokens encode an expiry timestamp signed with a secret key,
// so no server-side session state is needed.
type TokenSigner struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenSigner creates a token signer with the given secret and TTL.
// The secret must be at least 32 bytes.
func NewTokenSigner(secret []byte, ttl time.Duration) (*TokenSigner, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("token signing secret must be at least 32 bytes, got %d", len(secret))
	}
	return &TokenSigner{
		secret: secret,
		ttl:    ttl,
	}, nil
}

// Secret returns the raw signing key. Used by the share token subsystem
// which reuses this secret for AES-GCM encryption.
func (ts *TokenSigner) Secret() []byte {
	return ts.secret
}

// Create generates a new signed token with an expiry of now + TTL.
func (ts *TokenSigner) Create() string {
	expiry := time.Now().Add(ts.ttl).Unix()
	return ts.createWithExpiry(expiry)
}

func (ts *TokenSigner) createWithExpiry(expiry int64) string {
	eb := make([]byte, 8)
	binary.BigEndian.PutUint64(eb, uint64(expiry))

	mac := hmac.New(sha256.New, ts.secret)
	mac.Write(eb)
	sig := mac.Sum(nil)

	return hex.EncodeToString(eb) + "." + hex.EncodeToString(sig)
}

// Valid returns true if the token has a valid signature and hasn't expired.
func (ts *TokenSigner) Valid(token string) bool {
	if token == "" {
		return false
	}

	// Split into expiry.signature
	dot := -1
	for i, c := range token {
		if c == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return false
	}

	eb, err := hex.DecodeString(token[:dot])
	if err != nil || len(eb) != 8 {
		return false
	}

	sig, err := hex.DecodeString(token[dot+1:])
	if err != nil {
		return false
	}

	// Verify HMAC
	mac := hmac.New(sha256.New, ts.secret)
	mac.Write(eb)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return false
	}

	// Check expiry
	expiry := int64(binary.BigEndian.Uint64(eb))
	return time.Now().Unix() <= expiry
}

// GenerateSecret generates a cryptographically random 32-byte secret.
func GenerateSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
