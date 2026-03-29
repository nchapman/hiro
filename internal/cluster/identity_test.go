package cluster

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIdentity_GeneratesNew(t *testing.T) {
	dir := t.TempDir()

	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(id.PrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("private key wrong size: %d", len(id.PrivateKey))
	}
	if len(id.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("public key wrong size: %d", len(id.PublicKey))
	}
	if len(id.NodeID) != 64 {
		t.Fatalf("node ID wrong length: %d", len(id.NodeID))
	}

	// File should exist.
	data, err := os.ReadFile(filepath.Join(dir, identityFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != ed25519.SeedSize {
		t.Fatalf("seed file wrong size: %d", len(data))
	}
}

func TestLoadOrCreateIdentity_LoadsExisting(t *testing.T) {
	dir := t.TempDir()

	id1, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}

	id2, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}

	if id1.NodeID != id2.NodeID {
		t.Fatalf("node IDs differ: %s vs %s", id1.NodeID, id2.NodeID)
	}
	if !id1.PublicKey.Equal(id2.PublicKey) {
		t.Fatal("public keys differ")
	}
}

func TestLoadOrCreateIdentity_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, identityFile), []byte("too short"), 0600)

	_, err := LoadOrCreateIdentity(dir)
	if err == nil {
		t.Fatal("expected error for corrupt identity file")
	}
}

func TestIdentity_SignVerify(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("test message")
	sig := ed25519.Sign(id.PrivateKey, msg)
	if !ed25519.Verify(id.PublicKey, msg, sig) {
		t.Fatal("signature verification failed")
	}
}
