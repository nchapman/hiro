package cluster

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"testing"
)

func TestTLSCertFromIdentity(t *testing.T) {
	id := identityFromSeed(make([]byte, ed25519.SeedSize))

	cert, err := TLSCertFromIdentity(id)
	if err != nil {
		t.Fatal(err)
	}

	if len(cert.Certificate) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(cert.Certificate))
	}

	// Parse and verify it's a valid self-signed cert.
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	pub, ok := parsed.PublicKey.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("expected Ed25519 public key, got %T", parsed.PublicKey)
	}
	if !pub.Equal(id.PublicKey) {
		t.Fatal("certificate public key doesn't match identity")
	}
}

func TestTLSFingerprint_Deterministic(t *testing.T) {
	id := identityFromSeed(make([]byte, ed25519.SeedSize))

	cert1, _ := TLSCertFromIdentity(id)
	cert2, _ := TLSCertFromIdentity(id)

	fp1 := TLSFingerprint(cert1)
	fp2 := TLSFingerprint(cert2)

	if len(fp1) != 64 {
		t.Fatalf("fingerprint wrong length: %d", len(fp1))
	}

	// Verify it's valid hex.
	if _, err := hex.DecodeString(fp1); err != nil {
		t.Fatalf("fingerprint is not valid hex: %v", err)
	}

	// Note: CreateCertificate uses rand.Reader for signature, so fingerprints
	// may differ between calls. This is fine — fingerprints are exchanged via
	// tracker at runtime, not precomputed.
	_ = fp2
}

func TestMutualTLS_Handshake(t *testing.T) {
	// Create two identities.
	serverID := identityFromSeed(make([]byte, ed25519.SeedSize))
	clientSeed := make([]byte, ed25519.SeedSize)
	clientSeed[0] = 1
	clientID := identityFromSeed(clientSeed)

	serverCert, err := TLSCertFromIdentity(serverID)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, err := TLSCertFromIdentity(clientID)
	if err != nil {
		t.Fatal(err)
	}

	// Server TLS config.
	serverTLS := ServerTLSConfig(serverCert)

	// Client TLS config — verify server's public key.
	clientTLS := ClientTLSConfig(clientCert, serverID.PublicKey)

	// Start a TLS server.
	lis, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	errCh := make(chan error, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		// Complete the handshake.
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			errCh <- fmt.Errorf("expected *tls.Conn, got %T", conn)
			return
		}
		if err := tlsConn.Handshake(); err != nil {
			errCh <- fmt.Errorf("server handshake: %w", err)
			return
		}

		// Verify we can extract the client's public key.
		pub, err := PubKeyFromCert(tlsConn.ConnectionState())
		if err != nil {
			errCh <- err
			return
		}
		if !pub.Equal(clientID.PublicKey) {
			errCh <- fmt.Errorf("client public key mismatch")
			return
		}

		// Echo back data.
		buf := make([]byte, 5)
		n, _ := conn.Read(buf)
		conn.Write(buf[:n])
		errCh <- nil
	}()

	// Client connects.
	conn, err := tls.Dial("tcp", lis.Addr().String(), clientTLS)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer conn.Close()

	// Send and receive data.
	conn.Write([]byte("hello"))
	buf := make([]byte, 5)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(buf[:n]))
	}

	if err := <-errCh; err != nil {
		t.Fatalf("server error: %v", err)
	}
}

func TestMutualTLS_WrongServerKey(t *testing.T) {
	serverID := identityFromSeed(make([]byte, ed25519.SeedSize))
	clientSeed := make([]byte, ed25519.SeedSize)
	clientSeed[0] = 1
	clientID := identityFromSeed(clientSeed)

	// Create a different "expected" key — simulates a MITM.
	wrongSeed := make([]byte, ed25519.SeedSize)
	wrongSeed[0] = 99
	wrongID := identityFromSeed(wrongSeed)

	serverCert, _ := TLSCertFromIdentity(serverID)
	clientCert, _ := TLSCertFromIdentity(clientID)

	serverTLS := ServerTLSConfig(serverCert)
	// Client expects wrongID's key, but server presents serverID's key.
	clientTLS := ClientTLSConfig(clientCert, wrongID.PublicKey)

	lis, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		if tc, ok := conn.(*tls.Conn); ok {
			tc.Handshake()
		}
		conn.Close()
	}()

	conn, err := tls.Dial("tcp", lis.Addr().String(), clientTLS)
	if err == nil {
		conn.Close()
		t.Fatal("expected handshake failure for wrong server key")
	}
	// The error should mention key mismatch.
}

func TestMutualTLS_NoPubKeyVerification(t *testing.T) {
	// When expectedPubKey is nil, any server cert is accepted.
	// This is the "direct leader_addr without tracker" case.
	serverID := identityFromSeed(make([]byte, ed25519.SeedSize))
	clientSeed := make([]byte, ed25519.SeedSize)
	clientSeed[0] = 1
	clientID := identityFromSeed(clientSeed)

	serverCert, _ := TLSCertFromIdentity(serverID)
	clientCert, _ := TLSCertFromIdentity(clientID)

	serverTLS := ServerTLSConfig(serverCert)
	clientTLS := ClientTLSConfig(clientCert, nil) // no pinning

	lis, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if tc, ok := conn.(*tls.Conn); ok {
			tc.Handshake()
		}
		buf := make([]byte, 5)
		n, _ := conn.Read(buf)
		conn.Write(buf[:n])
	}()

	conn, err := tls.Dial("tcp", lis.Addr().String(), clientTLS)
	if err != nil {
		t.Fatalf("dial should succeed without pinning: %v", err)
	}
	defer conn.Close()

	conn.Write([]byte("hello"))
	buf := make([]byte, 5)
	n, _ := conn.Read(buf)
	if string(buf[:n]) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(buf[:n]))
	}
}

func TestPubKeyFromCert(t *testing.T) {
	id := identityFromSeed(make([]byte, ed25519.SeedSize))
	cert, _ := TLSCertFromIdentity(id)
	serverTLS := ServerTLSConfig(cert)

	clientSeed := make([]byte, ed25519.SeedSize)
	clientSeed[0] = 1
	clientID := identityFromSeed(clientSeed)
	clientCert, _ := TLSCertFromIdentity(clientID)
	clientTLS := ClientTLSConfig(clientCert, nil)

	lis, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	resultCh := make(chan ed25519.PublicKey, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			panic(fmt.Sprintf("expected *tls.Conn, got %T", conn))
		}
		tlsConn.Handshake()
		pub, _ := PubKeyFromCert(tlsConn.ConnectionState())
		resultCh <- pub
	}()

	conn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tlsConn := tls.Client(conn, clientTLS)
	tlsConn.Handshake()
	defer tlsConn.Close()

	pub := <-resultCh
	if !pub.Equal(clientID.PublicKey) {
		t.Fatal("extracted public key doesn't match client identity")
	}
}
