package cluster

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const identityFile = "identity.key"

// NodeIdentity holds an Ed25519 keypair used for tracker announcements
// and future cryptographic operations (e.g., WireGuard key derivation).
// The node ID is sha256(public_key), matching the tracker's derivation.
type NodeIdentity struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
	NodeID     string // hex(sha256(public_key))
}

// LoadOrCreateIdentity loads the node's Ed25519 identity from rootDir/identity.key,
// or generates a new one if it doesn't exist. The file stores the 32-byte seed.
func LoadOrCreateIdentity(rootDir string) (*NodeIdentity, error) {
	path := filepath.Join(rootDir, identityFile)

	seed, err := os.ReadFile(path)
	if err == nil {
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("identity.key has wrong size: got %d, want %d", len(seed), ed25519.SeedSize)
		}
		return identityFromSeed(seed), nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading identity.key: %w", err)
	}

	// Generate new identity.
	seed = make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generating identity seed: %w", err)
	}

	// Write with restrictive permissions — this is a private key.
	if err := os.WriteFile(path, seed, 0600); err != nil {
		return nil, fmt.Errorf("writing identity.key: %w", err)
	}

	return identityFromSeed(seed), nil
}

func identityFromSeed(seed []byte) *NodeIdentity {
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	hash := sha256.Sum256(pub)
	return &NodeIdentity{
		PrivateKey: priv,
		PublicKey:  pub,
		NodeID:     hex.EncodeToString(hash[:]),
	}
}
