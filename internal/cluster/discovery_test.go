package cluster

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testIdentity(t *testing.T) *NodeIdentity {
	t.Helper()
	return identityFromSeed(make([]byte, ed25519.SeedSize)) // deterministic zero seed
}

func TestDiscoveryClient_Announce(t *testing.T) {
	var called atomic.Int32
	identity := testIdentity(t)
	swarmCode := "test-swarm"
	expectedHash := sha256.Sum256([]byte(swarmCode))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)

		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/announce" {
			t.Errorf("expected /announce, got %s", r.URL.Path)
		}

		var req announceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode error: %v", err)
			http.Error(w, "bad json", 400)
			return
		}

		// Verify swarm hash.
		if req.SwarmHash != hex.EncodeToString(expectedHash[:]) {
			t.Errorf("wrong swarm hash: %s", req.SwarmHash)
		}

		// Verify public key.
		pubKeyBytes, err := base64.StdEncoding.DecodeString(req.PublicKey)
		if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
			t.Errorf("invalid public key")
		}

		// Verify signature.
		sigBytes, err := base64.StdEncoding.DecodeString(req.Signature)
		if err != nil {
			t.Errorf("invalid signature encoding")
		}
		if !ed25519.Verify(identity.PublicKey, req.signedMessage(), sigBytes) {
			t.Errorf("signature verification failed")
		}

		// Verify role and meta.
		if req.Role != "worker" {
			t.Errorf("expected role worker, got %s", req.Role)
		}
		if req.Meta["node_name"] != "test-node" {
			t.Errorf("expected node_name test-node, got %s", req.Meta["node_name"])
		}

		// Return a leader peer.
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "1.2.3.4",
			NodeID: identity.NodeID,
			Peers: []announceJSON{
				{
					NodeID:    "abcdef1234567890",
					PublicKey: "AAAA",
					Role:      "leader",
					Addresses: []string{"tcp://10.0.0.1:8081"},
					GRPCPort:  8081,
					LastSeen:  time.Now().Format(time.RFC3339),
				},
			},
		})
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  swarmCode,
		Role:       "worker",
		GRPCPort:   0,
		Identity:   identity,
		NodeName:   "test-node",
		Logger:     slog.Default(),
	})

	// Run a single announce cycle.
	ctx, cancel := context.WithCancel(context.Background())
	dc.announce(ctx)
	cancel()

	if called.Load() != 1 {
		t.Fatalf("expected 1 announce call, got %d", called.Load())
	}

	// Check cached peers.
	peers := dc.Peers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Role != "leader" {
		t.Errorf("expected leader, got %s", peers[0].Role)
	}
	if peers[0].GRPCPort != 8081 {
		t.Errorf("expected grpc port 8081, got %d", peers[0].GRPCPort)
	}

	// LeaderAddr should return the leader.
	addr := dc.LeaderAddr()
	if addr != "10.0.0.1:8081" {
		t.Errorf("expected 10.0.0.1:8081, got %s", addr)
	}
}

func TestDiscoveryClient_NoLeader(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "1.2.3.4",
			Peers:  []announceJSON{},
		})
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  "test",
		Role:       "worker",
		Identity:   testIdentity(t),
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	dc.announce(ctx)
	cancel()

	if addr := dc.LeaderAddr(); addr != "" {
		t.Errorf("expected empty leader addr, got %s", addr)
	}
}

func TestDiscoveryClient_WaitForLeader(t *testing.T) {
	// Return leader on the second announce call.
	callCount := atomic.Int32{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		resp := announceResponse{YourIP: "1.2.3.4"}
		if n >= 2 {
			resp.Peers = []announceJSON{
				{
					NodeID:    "leader-id",
					Role:      "leader",
					Addresses: []string{"tcp://10.0.0.1:8081"},
					GRPCPort:  8081,
					LastSeen:  time.Now().Format(time.RFC3339),
				},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  "test",
		Role:       "worker",
		Identity:   testIdentity(t),
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Prime the cache with an empty announce (no leader yet).
	dc.announce(ctx)

	// WaitForLeader re-announces every 5s — the second call will find the leader.
	addr, err := dc.WaitForLeader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.0.0.1:8081" {
		t.Errorf("expected 10.0.0.1:8081, got %s", addr)
	}
}

func TestDiscoveryClient_WaitForLeaderTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{YourIP: "1.2.3.4"})
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  "test",
		Role:       "worker",
		Identity:   testIdentity(t),
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := dc.WaitForLeader(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestDiscoveryClient_TrackerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", 500)
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  "test",
		Role:       "leader",
		GRPCPort:   8081,
		Identity:   testIdentity(t),
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	ctx := context.Background()
	dc.announce(ctx)

	// Should have no peers after error.
	if len(dc.Peers()) != 0 {
		t.Error("expected no peers after error")
	}
}

func TestDiscoveryClient_SwarmHashDerived(t *testing.T) {
	var receivedHash string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req announceRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedHash = req.SwarmHash
		json.NewEncoder(w).Encode(announceResponse{YourIP: "1.2.3.4"})
	}))
	defer ts.Close()

	swarmCode := "my-secret-swarm"
	expected := sha256.Sum256([]byte(swarmCode))

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  swarmCode,
		Role:       "leader",
		GRPCPort:   8081,
		Identity:   testIdentity(t),
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	dc.announce(context.Background())

	if receivedHash != hex.EncodeToString(expected[:]) {
		t.Errorf("swarm hash mismatch: got %s, want %s", receivedHash, hex.EncodeToString(expected[:]))
	}
}

func TestDiscoveryClient_MultiplePeers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "1.2.3.4",
			Peers: []announceJSON{
				{NodeID: "leader-1", Role: "leader", Addresses: []string{"tcp://10.0.0.1:8081"}, GRPCPort: 8081},
				{NodeID: "worker-1", Role: "worker", Addresses: []string{"tcp://10.0.0.2:0"}, GRPCPort: 0},
				{NodeID: "worker-2", Role: "worker", Addresses: []string{"tcp://10.0.0.3:0"}, GRPCPort: 0},
			},
		})
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  "test",
		Role:       "worker",
		Identity:   testIdentity(t),
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	dc.announce(context.Background())

	peers := dc.Peers()
	if len(peers) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(peers))
	}

	// LeaderAddr picks the leader.
	addr := dc.LeaderAddr()
	if addr != "10.0.0.1:8081" {
		t.Errorf("expected leader addr 10.0.0.1:8081, got %s", addr)
	}
}

func TestDiscoveryClient_TLSFingerprint(t *testing.T) {
	fingerprint := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	var receivedFingerprint string
	var receivedInPeers bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req announceRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedFingerprint = req.TLSFingerprint
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "1.2.3.4",
			Peers: []announceJSON{
				{
					NodeID:         "leader-1",
					Role:           "leader",
					Addresses:      []string{"tcp://10.0.0.1:8081"},
					GRPCPort:       8081,
					TLSFingerprint: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
				},
			},
		})
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL:     ts.URL,
		SwarmCode:      "test",
		Role:           "worker",
		Identity:       testIdentity(t),
		TLSFingerprint: fingerprint,
		NodeName:       "test",
		Logger:         slog.Default(),
	})

	dc.announce(context.Background())

	// Verify fingerprint was sent.
	if receivedFingerprint != fingerprint {
		t.Errorf("expected fingerprint %q, got %q", fingerprint, receivedFingerprint)
	}

	// Verify peer fingerprint was parsed.
	peers := dc.Peers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].TLSFingerprint != "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210" {
		t.Errorf("peer fingerprint = %q", peers[0].TLSFingerprint)
	}
	_ = receivedInPeers
}

func TestSignedMessage_Deterministic(t *testing.T) {
	req := &announceRequest{
		SwarmHash: "abc123",
		Timestamp: 1000,
		Role:      "leader",
		GRPCPort:  8081,
		Meta:      map[string]string{"b": "2", "a": "1"},
	}

	msg1 := req.signedMessage()
	msg2 := req.signedMessage()

	if string(msg1) != string(msg2) {
		t.Fatal("signed message not deterministic")
	}

	expected := "abc123\n1000\nleader\n8081\n\n\n\na=1\nb=2"
	if string(msg1) != expected {
		t.Errorf("unexpected signed message:\ngot:  %q\nwant: %q", string(msg1), expected)
	}
}

// TestSignedMessageCanonicalVector must produce the *exact* same bytes as the
// tracker's identical test in hiro-tracker/main_test.go. If these diverge,
// every real announce will fail signature verification. Keep both in sync.
func TestSignedMessageCanonicalVector(t *testing.T) {
	req := &announceRequest{
		SwarmHash:      "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Timestamp:      1700000000,
		Role:           "leader",
		GRPCPort:       8120,
		WGPubKey:       "",
		WGEndpoint:     "",
		TLSFingerprint: "cafecafecafecafecafecafecafecafecafecafecafecafecafecafecafecafe",
		Addresses:      []string{"tcp://100.64.1.5:8120", "tcp://192.168.1.5:8120"},
		Meta:           map[string]string{"node_name": "canonical-vector"},
	}
	want := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n" +
		"1700000000\n" +
		"leader\n" +
		"8120\n" +
		"\n" +
		"\n" +
		"cafecafecafecafecafecafecafecafecafecafecafecafecafecafecafecafe\n" +
		"tcp://100.64.1.5:8120\n" +
		"tcp://192.168.1.5:8120\n" +
		"node_name=canonical-vector"
	got := string(req.signedMessage())
	if got != want {
		t.Fatalf("canonical signed message mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestValidateAdvertiseAddressRejects(t *testing.T) {
	bad := []string{
		"",
		"tcp://127.0.0.1:8120",     // loopback
		"tcp://[::1]:8120",         // loopback v6
		"tcp://0.0.0.0:8120",       // unspecified
		"tcp://169.254.1.1:8120",   // link-local
		"tcp://[fe80::1]:8120",     // link-local v6
		"tcp://224.0.0.1:8120",     // multicast
		"http://10.0.0.1:8120",     // bad scheme
		"10.0.0.1:8120",            // no scheme
		"tcp://example.com:8120",   // hostname
		"tcp://10.0.0.1",           // no port
		"tcp://10.0.0.1:0",         // zero port
		"tcp://10.0.0.1:8120/path", // path included
	}
	for _, a := range bad {
		if msg := ValidateAdvertiseAddress(a); msg == "" {
			t.Errorf("expected rejection for %q", a)
		}
	}
	good := []string{
		"tcp://10.0.0.1:8120",
		"tcp4://192.168.1.5:8120",
		"tcp6://[2001:db8::1]:8120",
		"tcp://100.64.1.5:8120",
	}
	for _, a := range good {
		if msg := ValidateAdvertiseAddress(a); msg != "" {
			t.Errorf("expected %q to validate; got %q", a, msg)
		}
	}
}

func TestParsePeersPreservesAddresses(t *testing.T) {
	d := &DiscoveryClient{logger: slog.Default()}
	peers := d.parsePeers([]announceJSON{{
		NodeID:    "abc",
		Role:      "leader",
		Addresses: []string{"tcp://100.64.1.5:8120", "tcp://192.168.1.5:8120"},
		GRPCPort:  8120,
	}})
	if len(peers) != 1 || len(peers[0].Addresses) != 2 {
		t.Fatalf("expected Addresses preserved, got %+v", peers)
	}
}

func TestLeaderAddressesStripsScheme(t *testing.T) {
	d := &DiscoveryClient{logger: slog.Default()}
	d.peers = []DiscoveredPeer{{
		Role:      "leader",
		Addresses: []string{"tcp://100.64.1.5:8120", "tcp://192.168.1.5:8120"},
		GRPCPort:  8120,
		LastSeen:  time.Now(),
	}}
	got := d.LeaderAddresses()
	want := []string{"100.64.1.5:8120", "192.168.1.5:8120"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFilterValidAdvertiseAddressesDropsBad(t *testing.T) {
	in := []string{
		"tcp://10.0.0.1:8120",
		"tcp://127.0.0.1:8120", // loopback — dropped
		"not-a-url",            // malformed — dropped
		"tcp://192.168.1.5:8120",
	}
	got := filterValidAdvertiseAddresses(in, slog.Default())
	want := []string{"tcp://10.0.0.1:8120", "tcp://192.168.1.5:8120"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
