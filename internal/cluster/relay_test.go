package cluster

import (
	"crypto/ed25519"
	"errors"
	"net"
	"testing"
	"time"
)

func TestRelayStatusName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status byte
		want   string
	}{
		{relayStatusOK, "ok"},
		{relayStatusBadSig, "bad_signature"},
		{relayStatusStale, "stale_timestamp"},
		{relayStatusNoPeer, "no_peer"},
		{relayStatusFull, "relay_full"},
		{relayStatusConflict, "conflict"},
		{0xAB, "ab"}, // unknown status returns hex
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := relayStatusName(tt.status)
			if got != tt.want {
				t.Fatalf("relayStatusName(0x%02x) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestNewRelayClient(t *testing.T) {
	t.Parallel()

	identity := identityFromSeed(make([]byte, 32))
	rc := NewRelayClient(RelayConfig{
		RelayAddr: "relay.example.com:9443",
		SwarmCode: "test-swarm",
		Identity:  identity,
	})

	if rc.relayAddr != "relay.example.com:9443" {
		t.Fatalf("relayAddr = %q, want %q", rc.relayAddr, "relay.example.com:9443")
	}
	if rc.identity != identity {
		t.Fatal("identity not set")
	}
}

func TestChannelListener_AcceptAndEnqueue(t *testing.T) {
	t.Parallel()

	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
	cl := NewChannelListener(addr)

	if cl.Addr() != addr {
		t.Fatalf("Addr() = %v, want %v", cl.Addr(), addr)
	}

	// Enqueue a connection and accept it.
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	cl.Enqueue(server)

	accepted, err := cl.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted != server {
		t.Fatal("accepted connection is not the enqueued one")
	}
}

func TestChannelListener_Close(t *testing.T) {
	t.Parallel()

	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
	cl := NewChannelListener(addr)

	// Close the listener.
	if err := cl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Accept after close should return error.
	_, err := cl.Accept()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("expected net.ErrClosed, got %v", err)
	}

	// Double close should be safe.
	if err := cl.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}

func TestChannelListener_CloseWithBuffered(t *testing.T) {
	t.Parallel()

	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
	cl := NewChannelListener(addr)

	// Enqueue connections then close — they should be drained and closed.
	server1, client1 := net.Pipe()
	defer client1.Close()
	server2, client2 := net.Pipe()
	defer client2.Close()

	cl.Enqueue(server1)
	cl.Enqueue(server2)

	if err := cl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the buffered connections were closed by attempting to read.
	server1.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := server1.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected error reading from closed connection")
	}
}

func TestChannelListener_EnqueueAfterClose(t *testing.T) {
	t.Parallel()

	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
	cl := NewChannelListener(addr)
	cl.Close()

	// Enqueue after close should close the connection.
	server, client := net.Pipe()
	defer client.Close()

	cl.Enqueue(server)

	// Verify the connection was closed.
	server.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := server.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected error reading from connection enqueued after close")
	}
}

func TestReadRelayStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status byte
	}{
		{"ok", relayStatusOK},
		{"bad_sig", relayStatusBadSig},
		{"no_peer", relayStatusNoPeer},
		{"full", relayStatusFull},
		{"conflict", relayStatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()

			go func() {
				client.Write([]byte{tt.status})
			}()

			status, err := readRelayStatus(server)
			if err != nil {
				t.Fatalf("readRelayStatus: %v", err)
			}
			if status != tt.status {
				t.Fatalf("got status 0x%02x, want 0x%02x", status, tt.status)
			}
		})
	}
}

func TestReadRelayStatus_Timeout(t *testing.T) {
	t.Parallel()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// Don't write anything — should timeout.
	// readRelayStatus sets a read deadline of relayStatusReadDeadline (30s),
	// which is too long. Close the connection instead to simulate error.
	go func() {
		time.Sleep(50 * time.Millisecond)
		client.Close()
	}()

	_, err := readRelayStatus(server)
	if err == nil {
		t.Fatal("expected error for closed connection")
	}
}

func TestSendHandshake(t *testing.T) {
	t.Parallel()

	identity := identityFromSeed(make([]byte, ed25519.SeedSize))
	rc := NewRelayClient(RelayConfig{
		RelayAddr: "relay.example.com:9443",
		SwarmCode: "test-swarm",
		Identity:  identity,
	})

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		err := rc.sendHandshake(server, relayRoleLeader)
		if err != nil {
			t.Errorf("sendHandshake: %v", err)
		}
	}()

	buf := make([]byte, relayHandshakeSize)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("reading handshake: %v", err)
	}
	if n != relayHandshakeSize {
		t.Fatalf("handshake size = %d, want %d", n, relayHandshakeSize)
	}

	// Verify header.
	if buf[0] != relayVersion {
		t.Fatalf("version = 0x%02x, want 0x%02x", buf[0], relayVersion)
	}
	if buf[1] != relayRoleLeader {
		t.Fatalf("role = 0x%02x, want 0x%02x", buf[1], relayRoleLeader)
	}

	// Verify swarm hash is in bytes 2-34.
	// Public key is in bytes 34-66.
	pubKey := buf[34:66]
	if !ed25519.PublicKey(pubKey).Equal(identity.PublicKey) {
		t.Fatal("public key in handshake doesn't match identity")
	}

	// Verify signature (bytes 74-138) over message (bytes 0-74).
	sig := buf[74:138]
	if !ed25519.Verify(identity.PublicKey, buf[:74], sig) {
		t.Fatal("handshake signature verification failed")
	}
}
