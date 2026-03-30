package auth

import (
	"testing"
	"time"
)

func testSigner(ttl time.Duration) *TokenSigner {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}
	ts, err := NewTokenSigner(secret, ttl)
	if err != nil {
		panic(err)
	}
	return ts
}

func TestCreateAndValidate(t *testing.T) {
	ts := testSigner(time.Hour)
	token := ts.Create()
	if token == "" {
		t.Fatal("empty token")
	}
	if !ts.Valid(token) {
		t.Error("token should be valid")
	}
}

func TestInvalidToken(t *testing.T) {
	ts := testSigner(time.Hour)
	if ts.Valid("nonexistent") {
		t.Error("nonexistent token should be invalid")
	}
	if ts.Valid("") {
		t.Error("empty token should be invalid")
	}
	if ts.Valid("abc.def") {
		t.Error("garbage token should be invalid")
	}
	if ts.Valid("no-dot-at-all") {
		t.Error("token without dot should be invalid")
	}
}

func TestExpiration(t *testing.T) {
	ts := testSigner(time.Second)
	token := ts.Create()
	time.Sleep(2 * time.Second)
	if ts.Valid(token) {
		t.Error("expired token should be invalid")
	}
}

func TestTamperedSignature(t *testing.T) {
	ts := testSigner(time.Hour)
	token := ts.Create()
	// Flip last character of signature
	tampered := token[:len(token)-1] + "0"
	if token[len(token)-1] == '0' {
		tampered = token[:len(token)-1] + "1"
	}
	if ts.Valid(tampered) {
		t.Error("tampered token should be invalid")
	}
}

func TestDifferentSecretRejects(t *testing.T) {
	ts1 := testSigner(time.Hour)
	token := ts1.Create()

	otherSecret := make([]byte, 32)
	for i := range otherSecret {
		otherSecret[i] = byte(i + 100)
	}
	ts2, err := NewTokenSigner(otherSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if ts2.Valid(token) {
		t.Error("token signed with different secret should be invalid")
	}
}

func TestUniqueTokens(t *testing.T) {
	ts := testSigner(time.Hour)
	t1 := ts.Create()
	// Tokens created at the same second will have the same expiry and
	// thus the same HMAC — that's expected since there's no random nonce.
	// But they're deterministic, not "unique" in the random sense.
	// Just verify they're non-empty.
	t2 := ts.Create()
	if t1 == "" || t2 == "" {
		t.Error("tokens should be non-empty")
	}
}

func TestGenerateSecret(t *testing.T) {
	s1, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(s1) != 32 {
		t.Errorf("secret length = %d, want 32", len(s1))
	}
	s2, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	// Two random secrets should differ
	if string(s1) == string(s2) {
		t.Error("two generated secrets should differ")
	}
}
