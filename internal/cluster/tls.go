package cluster

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// TLSCertFromIdentity generates a self-signed X.509 certificate from an Ed25519
// identity key. The certificate is used for mutual TLS between cluster nodes.
//
// The TLS fingerprint (SHA-256 of the DER-encoded certificate) is deterministic
// for a given key — it only depends on the public key, serial number, and validity
// period, all of which are fixed.
func TLSCertFromIdentity(identity *NodeIdentity) (tls.Certificate, error) {
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, identity.PublicKey, identity.PrivateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("creating self-signed certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  identity.PrivateKey,
	}, nil
}

// TLSFingerprint returns the hex-encoded SHA-256 hash of a DER-encoded certificate.
func TLSFingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	h := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(h[:])
}

// ServerTLSConfig creates a TLS config for the leader's gRPC server.
// It requires client certificates (mutual TLS) and uses a custom verifier
// that accepts any self-signed cert — authentication is handled by join tokens,
// TLS provides encryption and identity binding.
func ServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2"}, // required for gRPC over TLS
	}
}

// ClientTLSConfig creates a TLS config for a worker connecting to the leader.
// If expectedPubKey is non-nil, the leader's certificate public key is verified
// against it (the key was learned from the tracker or config).
// If expectedPubKey is nil, any valid TLS handshake is accepted — the join token
// provides authentication in this case.
func ClientTLSConfig(clientCert tls.Certificate, expectedPubKey ed25519.PublicKey) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{clientCert},
		InsecureSkipVerify: true, // we do our own verification below
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"h2"}, // required for gRPC over TLS
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("server presented no certificate")
			}

			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("parsing server certificate: %w", err)
			}

			// If we know the expected public key (from tracker), verify it.
			if expectedPubKey != nil {
				serverPub, ok := cert.PublicKey.(ed25519.PublicKey)
				if !ok {
					return fmt.Errorf("server certificate uses %T, expected Ed25519", cert.PublicKey)
				}
				if !serverPub.Equal(expectedPubKey) {
					return errors.New("server public key does not match expected key from tracker")
				}
			}

			return nil
		},
	}
}

// PubKeyFromCert extracts the Ed25519 public key from a peer's TLS certificate.
// Used by the leader to identify which node connected, for logging and future
// service proxy routing.
func PubKeyFromCert(state tls.ConnectionState) (ed25519.PublicKey, error) {
	if len(state.PeerCertificates) == 0 {
		return nil, errors.New("peer presented no certificate")
	}
	pub, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("peer certificate uses %T, expected Ed25519", state.PeerCertificates[0].PublicKey)
	}
	return pub, nil
}
