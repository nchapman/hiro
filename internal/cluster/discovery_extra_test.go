package cluster

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDiscoveryClient_RelayURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{
			YourIP:   "1.2.3.4",
			RelayURL: "relay.example.com:9443",
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

	if got := dc.RelayURL(); got != "relay.example.com:9443" {
		t.Fatalf("RelayURL() = %q, want %q", got, "relay.example.com:9443")
	}
}

func TestDiscoveryClient_YourIP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "203.0.113.42",
		})
	}))
	defer ts.Close()

	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: ts.URL,
		SwarmCode:  "test",
		Role:       "leader",
		Identity:   testIdentity(t),
		NodeName:   "test",
		Logger:     slog.Default(),
	})

	dc.announce(context.Background())

	if got := dc.YourIP(); got != "203.0.113.42" {
		t.Fatalf("YourIP() = %q, want %q", got, "203.0.113.42")
	}
}

func TestDiscoveryClient_Announce_PublicMethod(t *testing.T) {
	// Verify that the public Announce method works the same as announce.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "1.2.3.4",
			Peers: []announceJSON{
				{NodeID: "leader-1", Role: "leader", Endpoint: "10.0.0.1", GRPCPort: 8081},
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

	dc.Announce(context.Background())

	if addr := dc.LeaderAddr(); addr != "10.0.0.1:8081" {
		t.Fatalf("LeaderAddr() = %q, want %q", addr, "10.0.0.1:8081")
	}
}

func TestDiscoveryClient_Run_CancelsCleanly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{YourIP: "1.2.3.4"})
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

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		dc.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run exited cleanly.
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestCheckSwarm_LeaderFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "1.2.3.4",
			Peers: []announceJSON{
				{
					NodeID:   "abcdef1234567890abcdef",
					Role:     "leader",
					Endpoint: "10.0.0.1",
					GRPCPort: 8081,
					LastSeen: time.Now().Format(time.RFC3339),
				},
			},
		})
	}))
	defer ts.Close()

	result, err := CheckSwarm(context.Background(), ts.URL, "test-swarm", testIdentity(t), slog.Default())
	if err != nil {
		t.Fatalf("CheckSwarm: %v", err)
	}
	if !result.LeaderFound {
		t.Fatal("expected LeaderFound=true")
	}
	if result.LeaderName == "" {
		t.Fatal("expected non-empty LeaderName")
	}
	// Name should be truncated to discoveryNodeIDDisplayLen.
	if len(result.LeaderName) > discoveryNodeIDDisplayLen {
		t.Fatalf("LeaderName too long: %q (max %d)", result.LeaderName, discoveryNodeIDDisplayLen)
	}
}

func TestCheckSwarm_NoLeader(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(announceResponse{
			YourIP: "1.2.3.4",
			Peers:  []announceJSON{},
		})
	}))
	defer ts.Close()

	result, err := CheckSwarm(context.Background(), ts.URL, "test-swarm", testIdentity(t), slog.Default())
	if err != nil {
		t.Fatalf("CheckSwarm: %v", err)
	}
	if result.LeaderFound {
		t.Fatal("expected LeaderFound=false")
	}
}
