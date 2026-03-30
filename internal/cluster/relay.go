package cluster

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// Relay protocol constants — must match hiro-relay server.
const (
	relayVersion       = 0x01
	relayRoleLeader    = 0x01
	relayRoleWorker    = 0x02
	relayHandshakeSize = 138

	relayStatusOK       = 0x00
	relayStatusBadSig   = 0x01
	relayStatusStale    = 0x02
	relayStatusNoPeer   = 0x03
	relayStatusFull     = 0x04
	relayStatusConflict = 0x05
	relayNotifyIncoming = 0xFF

	maxPendingRelayConns = 16
)

// RelayClient manages a leader's connection to the relay server.
// It maintains a persistent control connection and opens data connections
// on demand when workers need to connect through the relay.
type RelayClient struct {
	relayAddr string
	swarmHash [32]byte
	identity  *NodeIdentity
	logger    *slog.Logger

	mu       sync.Mutex
	ctrlConn net.Conn
}

// RelayConfig configures the relay client.
type RelayConfig struct {
	RelayAddr string // e.g. "relay.hellohiro.ai:9443"
	SwarmCode string // raw swarm code (hashed before sending)
	Identity  *NodeIdentity
	Logger    *slog.Logger
}

// NewRelayClient creates a relay client for a leader.
func NewRelayClient(cfg RelayConfig) *RelayClient {
	hash := sha256.Sum256([]byte(cfg.SwarmCode))
	return &RelayClient{
		relayAddr: cfg.RelayAddr,
		swarmHash: hash,
		identity:  cfg.Identity,
		logger:    cfg.Logger,
	}
}

// Run maintains a persistent control connection to the relay.
// When notified of incoming workers (0xFF byte), it opens a new data
// connection that gets paired with the worker. The paired connection
// is passed to onConnection for the caller to use (typically as a gRPC conn).
// Blocks until ctx is done.
func (rc *RelayClient) Run(ctx context.Context, onConnection func(net.Conn)) {
	backoff := 5 * time.Second
	for {
		wasConnected, err := rc.connectAndServe(ctx, onConnection)
		if ctx.Err() != nil {
			return
		}

		// Reset backoff after a healthy session; increase on immediate failures.
		if wasConnected {
			backoff = 5 * time.Second
		} else if backoff < 120*time.Second {
			backoff = backoff * 2
		}

		rc.logger.Warn("relay control connection lost, reconnecting...", "error", err, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}
}

func (rc *RelayClient) connectAndServe(ctx context.Context, onConnection func(net.Conn)) (wasConnected bool, err error) {
	conn, err := net.DialTimeout("tcp", rc.relayAddr, 10*time.Second)
	if err != nil {
		return false, fmt.Errorf("dialing relay: %w", err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(15 * time.Second)
	}

	// Send leader handshake.
	if err := rc.sendHandshake(conn, relayRoleLeader); err != nil {
		conn.Close()
		return false, err
	}

	status, err := readRelayStatus(conn)
	if err != nil {
		conn.Close()
		return false, err
	}
	if status == relayStatusConflict {
		conn.Close()
		return false, fmt.Errorf("relay: leader already registered for this swarm")
	}
	if status != relayStatusOK {
		conn.Close()
		return false, fmt.Errorf("relay registration failed: status %d", status)
	}

	rc.mu.Lock()
	rc.ctrlConn = conn
	rc.mu.Unlock()

	rc.logger.Info("registered with relay", "relay", rc.relayAddr)

	// Per-connection context: cancels both helper goroutines when
	// connectAndServe returns (for any reason).
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	// Close connection on context cancellation.
	go func() {
		<-connCtx.Done()
		conn.Close()
	}()

	// Keepalive: send bytes every 15s to prevent NAT/proxy idle timeouts.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := conn.Write([]byte{0x00}); err != nil {
					rc.logger.Warn("relay keepalive write failed", "error", err)
					conn.Close() // unblocks the read loop
					return
				}
				conn.SetWriteDeadline(time.Time{})
			case <-connCtx.Done():
				return
			}
		}
	}()

	// Semaphore to cap concurrent incoming connection handlers.
	sem := make(chan struct{}, maxPendingRelayConns)

	buf := make([]byte, 1)
	for {
		// No read deadline — keepalive write failures and context cancel
		// close the connection, which unblocks this read.
		_, err := conn.Read(buf)
		if err != nil {
			rc.mu.Lock()
			rc.ctrlConn = nil
			rc.mu.Unlock()
			return true, fmt.Errorf("control connection read: %w", err)
		}

		if buf[0] == relayNotifyIncoming {
			select {
			case sem <- struct{}{}:
				go func() {
					defer func() { <-sem }()
					rc.handleIncoming(connCtx, onConnection)
				}()
			default:
				rc.logger.Warn("relay: too many pending connections, dropping notification")
			}
		}
	}
}

func (rc *RelayClient) handleIncoming(ctx context.Context, onConnection func(net.Conn)) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", rc.relayAddr)
	if err != nil {
		rc.logger.Error("failed to dial relay for data connection", "error", err)
		return
	}

	if err := rc.sendHandshake(conn, relayRoleLeader); err != nil {
		conn.Close()
		rc.logger.Error("relay data handshake failed", "error", err)
		return
	}

	status, err := readRelayStatus(conn)
	if err != nil {
		conn.Close()
		rc.logger.Error("relay data status read failed", "error", err)
		return
	}
	if status != relayStatusOK {
		conn.Close()
		rc.logger.Error("relay data connection rejected", "status", status)
		return
	}

	rc.logger.Info("relay: paired with worker")
	onConnection(conn)
}

func (rc *RelayClient) sendHandshake(conn net.Conn, role byte) error {
	var buf [relayHandshakeSize]byte
	buf[0] = relayVersion
	buf[1] = role
	copy(buf[2:34], rc.swarmHash[:])
	copy(buf[34:66], rc.identity.PublicKey)
	binary.BigEndian.PutUint64(buf[66:74], uint64(time.Now().Unix()))
	sig := ed25519.Sign(rc.identity.PrivateKey, buf[:74])
	copy(buf[74:138], sig)

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := conn.Write(buf[:])
	conn.SetWriteDeadline(time.Time{})
	return err
}

// DialRelay connects to the relay as a worker and returns the paired connection.
// Used by workers for the relay leg of happy eyeballs.
func DialRelay(ctx context.Context, relayAddr string, swarmCode string, identity *NodeIdentity) (net.Conn, error) {
	hash := sha256.Sum256([]byte(swarmCode))

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("dialing relay: %w", err)
	}

	// Send worker handshake.
	var buf [relayHandshakeSize]byte
	buf[0] = relayVersion
	buf[1] = relayRoleWorker
	copy(buf[2:34], hash[:])
	copy(buf[34:66], identity.PublicKey)
	binary.BigEndian.PutUint64(buf[66:74], uint64(time.Now().Unix()))
	sig := ed25519.Sign(identity.PrivateKey, buf[:74])
	copy(buf[74:138], sig)

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(buf[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("writing handshake: %w", err)
	}
	conn.SetWriteDeadline(time.Time{})

	status, err := readRelayStatus(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("reading status: %w", err)
	}
	if status != relayStatusOK {
		conn.Close()
		return nil, fmt.Errorf("relay pairing failed: status %d (%s)", status, relayStatusName(status))
	}

	return conn, nil
}

// SelfTestReachability checks if this node's gRPC server is reachable at the
// given address by performing a TLS handshake. A bare TCP connect would give
// false positives from captive portals or middleboxes.
func SelfTestReachability(addr string, tlsCert tls.Certificate) bool {
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{tlsCert},
		InsecureSkipVerify: true, // we're dialing ourselves
		NextProtos:         []string{"h2"},
	}
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 3 * time.Second},
		"tcp", addr, tlsCfg,
	)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func readRelayStatus(conn net.Conn) (byte, error) {
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var status [1]byte
	if _, err := io.ReadFull(conn, status[:]); err != nil {
		return 0, err
	}
	conn.SetReadDeadline(time.Time{})
	return status[0], nil
}

func isConflictError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already registered")
}

func relayStatusName(status byte) string {
	switch status {
	case relayStatusOK:
		return "ok"
	case relayStatusBadSig:
		return "bad_signature"
	case relayStatusStale:
		return "stale_timestamp"
	case relayStatusNoPeer:
		return "no_peer"
	case relayStatusFull:
		return "relay_full"
	case relayStatusConflict:
		return "conflict"
	default:
		return hex.EncodeToString([]byte{status})
	}
}

// ChannelListener is a net.Listener backed by a channel.
// Relayed connections are enqueued via Enqueue() and consumed by gRPC's Serve().
type ChannelListener struct {
	ch   chan net.Conn
	done chan struct{}
	addr net.Addr
}

func NewChannelListener(addr net.Addr) *ChannelListener {
	return &ChannelListener{
		ch:   make(chan net.Conn, 16),
		done: make(chan struct{}),
		addr: addr,
	}
}

func (l *ChannelListener) Enqueue(conn net.Conn) {
	select {
	case l.ch <- conn:
	case <-l.done:
		conn.Close()
	}
}

func (l *ChannelListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *ChannelListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *ChannelListener) Addr() net.Addr {
	return l.addr
}
