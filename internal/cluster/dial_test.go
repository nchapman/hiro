package cluster

import (
	"context"
	"net"
	"testing"
)

// startListener opens a loopback TCP listener and returns its address and a
// cleanup func that closes it. Accepts are drained so SYNs complete cleanly.
func startListener(t *testing.T) (addr string, stop func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				close(done)
				return
			}
			_ = c.Close()
		}
	}()
	return l.Addr().String(), func() {
		_ = l.Close()
		<-done
	}
}

// unusedLocalAddr reserves a port, closes the listener, and returns the
// address — connections to it should fail with ECONNREFUSED.
func unusedLocalAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestDialLeaderFallsThroughToLiveAddress(t *testing.T) {
	liveAddr, stop := startListener(t)
	defer stop()

	cache := NewWinnerCache()
	conn, err := DialLeader(context.Background(), DialLeaderConfig{
		Addresses: []string{unusedLocalAddr(t), liveAddr},
		CacheKey:  "leader-1",
		Cache:     cache,
	})
	if err != nil {
		t.Fatalf("DialLeader: %v", err)
	}
	_ = conn.Close()

	if got := cache.Get("leader-1"); got != liveAddr {
		t.Fatalf("expected cache to record %q, got %q", liveAddr, got)
	}
}

func TestDialLeaderUsesCachedWinnerFirst(t *testing.T) {
	cachedAddr, stopCached := startListener(t)
	defer stopCached()
	fallbackAddr, stopFallback := startListener(t)
	defer stopFallback()

	cache := NewWinnerCache()
	cache.Set("leader-1", cachedAddr)

	// Cache hit should win even when a perfectly-good fallback is also listed.
	// We verify by checking the RemoteAddr of the returned connection.
	conn, err := DialLeader(context.Background(), DialLeaderConfig{
		Addresses: []string{fallbackAddr}, // cachedAddr intentionally absent
		CacheKey:  "leader-1",
		Cache:     cache,
	})
	if err != nil {
		t.Fatalf("DialLeader: %v", err)
	}
	defer conn.Close()

	if got := conn.RemoteAddr().String(); got != cachedAddr {
		t.Fatalf("expected connection to cached addr %q, got %q", cachedAddr, got)
	}
}

func TestDialLeaderClearsCacheOnStaleWinner(t *testing.T) {
	liveAddr, stop := startListener(t)
	defer stop()

	cache := NewWinnerCache()
	cache.Set("leader-1", unusedLocalAddr(t)) // stale

	conn, err := DialLeader(context.Background(), DialLeaderConfig{
		Addresses: []string{liveAddr},
		CacheKey:  "leader-1",
		Cache:     cache,
	})
	if err != nil {
		t.Fatalf("DialLeader: %v", err)
	}
	_ = conn.Close()

	if got := cache.Get("leader-1"); got != liveAddr {
		t.Fatalf("expected cache refreshed to %q, got %q", liveAddr, got)
	}
}

func TestDialLeaderAllFailNoRelay(t *testing.T) {
	_, err := DialLeader(context.Background(), DialLeaderConfig{
		Addresses: []string{unusedLocalAddr(t), unusedLocalAddr(t)},
	})
	if err == nil {
		t.Fatal("expected error when no addresses reachable and no relay")
	}
}

func TestDialLeaderEmptyAddressesNoRelay(t *testing.T) {
	_, err := DialLeader(context.Background(), DialLeaderConfig{})
	if err == nil {
		t.Fatal("expected error with empty addresses and no relay")
	}
}
