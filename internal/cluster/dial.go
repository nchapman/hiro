package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	// leaderDialTimeout bounds each direct-dial attempt in the race.
	leaderDialTimeout = 2 * time.Second

	// relayStagger is how long direct dials get a head start before relay
	// fires. Long enough for a fast LAN / Tailscale dial to finish and cancel
	// the relay attempt; short enough that strict-NAT hosts don't sit idle.
	relayStagger = 500 * time.Millisecond

	// cachedSoloTimeout bounds the optimistic "try last winner first" attempt.
	// If the cached winner is suddenly dead, we don't want to block the race.
	// Worst-case reconnect latency is roughly cachedSoloTimeout +
	// leaderDialTimeout (~2.5s) before we return an error or fall to relay.
	cachedSoloTimeout = 500 * time.Millisecond
)

// WinnerCache remembers the last address that successfully dialed a given key.
// In-memory only — resets on restart. Keying by node ID (rather than by
// address) means cache entries survive a leader's address list changing.
type WinnerCache struct {
	mu      sync.RWMutex
	winners map[string]string
}

// NewWinnerCache returns an empty cache.
func NewWinnerCache() *WinnerCache {
	return &WinnerCache{winners: make(map[string]string)}
}

// Get returns the cached winner for key, or "" if none.
func (c *WinnerCache) Get(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.winners[key]
}

// Set records a successful dial.
func (c *WinnerCache) Set(key, addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.winners[key] = addr
}

// Clear drops the cached winner for key.
func (c *WinnerCache) Clear(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.winners, key)
}

// DialLeaderConfig parameters for DialLeader.
type DialLeaderConfig struct {
	Addresses []string // bare "host:port" entries; order is not significant
	RelayAddr string   // empty to skip relay fallback
	SwarmCode string   // required if RelayAddr is set
	Identity  *NodeIdentity
	CacheKey  string // identifier for winner cache (typically the leader's node ID)
	Cache     *WinnerCache
	Logger    *slog.Logger
}

// DialLeader picks a reachable address for the leader and returns a net.Conn.
//
// Strategy (happy-eyeballs-style, parallel):
//
//  1. Cache-hit fast path. If the winner cache has an entry for CacheKey,
//     dial it solo with a short budget. Success returns immediately.
//  2. Race. Kick off every direct address in parallel at t=0. Relay fires at
//     t=500ms if no direct dial has won — the stagger gives fast LAN/Tailscale
//     connects a chance to win before we spend relay resources.
//  3. First success wins. Remaining attempts are canceled and any late
//     successful connection is closed so we don't leak.
//
// Parallel dialing is safe because: the address list is capped at 8 and
// validated (no loopback / metadata IPs); mTLS layered above prevents any
// spoofed-address impersonation; stray SYNs are cheap.
func DialLeader(ctx context.Context, cfg DialLeaderConfig) (net.Conn, error) {
	if conn, ok := tryCachedWinner(ctx, cfg); ok {
		return conn, nil
	}
	return raceDial(ctx, cfg)
}

// tryCachedWinner attempts a solo dial against the cached winner. Returns the
// connection on success; otherwise clears the cache and returns false so the
// caller falls through to the full race.
func tryCachedWinner(ctx context.Context, cfg DialLeaderConfig) (net.Conn, bool) {
	if cfg.Cache == nil || cfg.CacheKey == "" {
		return nil, false
	}
	winner := cfg.Cache.Get(cfg.CacheKey)
	if winner == "" {
		return nil, false
	}
	if conn, err := dialOne(ctx, winner, cachedSoloTimeout); err == nil {
		cfg.logConnected("cached", winner)
		return conn, true
	}
	cfg.Cache.Clear(cfg.CacheKey)
	return nil, false
}

// raceDial runs the happy-eyeballs race: all direct addresses start at t=0;
// relay fires at t=relayStagger. First success wins; stragglers are canceled
// and any late connection is closed so we don't leak fds.
func raceDial(ctx context.Context, cfg DialLeaderConfig) (net.Conn, error) {
	hasRelay := cfg.RelayAddr != "" && cfg.SwarmCode != "" && cfg.Identity != nil
	attempts := len(cfg.Addresses)
	if hasRelay {
		attempts++
	}
	if attempts == 0 {
		return nil, fmt.Errorf("no leader addresses to dial")
	}

	raceCtx, cancelRace := context.WithCancel(ctx)
	defer cancelRace()

	ch := make(chan result, attempts)
	for _, addr := range cfg.Addresses {
		go func(addr string) {
			conn, err := dialOne(raceCtx, addr, leaderDialTimeout)
			ch <- result{conn, err, "direct", addr}
		}(addr)
	}
	if hasRelay {
		go func() {
			select {
			case <-time.After(relayStagger):
			case <-raceCtx.Done():
				ch <- result{nil, raceCtx.Err(), "relay", cfg.RelayAddr}
				return
			}
			conn, err := DialRelay(raceCtx, cfg.RelayAddr, cfg.SwarmCode, cfg.Identity)
			ch <- result{conn, err, "relay", cfg.RelayAddr}
		}()
	}

	var firstErr error
	for i := range attempts {
		r := <-ch
		if r.err == nil {
			// Relay wins aren't pinned — relay is an expensive fallback,
			// not a preferred route. We want subsequent reconnects to keep
			// trying direct addresses first.
			if cfg.Cache != nil && cfg.CacheKey != "" && r.via == "direct" {
				cfg.Cache.Set(cfg.CacheKey, r.addr)
			}
			cfg.logConnected(r.via, r.addr)
			cancelRace()
			go drainAndClose(ch, attempts-i-1)
			return r.conn, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s %s: %w", r.via, r.addr, r.err)
		}
	}
	return nil, fmt.Errorf("all connection attempts failed: %w", firstErr)
}

// drainAndClose reads n remaining results and closes any stray successful
// connections. Called in a goroutine so the winning path doesn't wait.
func drainAndClose(ch <-chan result, n int) {
	for range n {
		r := <-ch
		if r.conn != nil {
			_ = r.conn.Close()
		}
	}
}

type result struct {
	conn net.Conn
	err  error
	via  string
	addr string
}

func (cfg DialLeaderConfig) logConnected(via, addr string) {
	if cfg.Logger == nil {
		return
	}
	cfg.Logger.Info("connected to leader", "via", via, "addr", addr)
}

func dialOne(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
}
