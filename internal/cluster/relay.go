package cluster

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
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
)

// RelayClient manages a leader's connection to the relay server.
// It maintains a persistent control connection and opens data connections
// on demand when workers need to connect through the relay.
type RelayClient struct {
	relayAddr  string
	swarmHash  [32]byte
	identity   *NodeIdentity
	logger     *slog.Logger

	mu       sync.Mutex
	ctrlConn net.Conn
	done     chan struct{}
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
		done:      make(chan struct{}),
	}
}

// Run maintains a persistent control connection to the relay.
// When notified of incoming workers (0xFF byte), it opens a new data
// connection that gets paired with the worker. The paired connection
// is passed to onConnection for the caller to use (typically as a gRPC conn).
// Blocks until ctx is done.
func (rc *RelayClient) Run(ctx context.Context, onConnection func(net.Conn)) {
	for {
		err := rc.connectAndServe(ctx, onConnection)
		if ctx.Err() != nil {
			return
		}
		rc.logger.Warn("relay control connection lost, reconnecting...", "error", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (rc *RelayClient) connectAndServe(ctx context.Context, onConnection func(net.Conn)) error {
	conn, err := net.DialTimeout("tcp", rc.relayAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dialing relay: %w", err)
	}

	// Send leader handshake.
	if err := rc.sendHandshake(conn, relayRoleLeader); err != nil {
		conn.Close()
		return err
	}

	status, err := readRelayStatus(conn)
	if err != nil {
		conn.Close()
		return err
	}
	if status == relayStatusConflict {
		conn.Close()
		return fmt.Errorf("relay: leader already registered for this swarm")
	}
	if status != relayStatusOK {
		conn.Close()
		return fmt.Errorf("relay registration failed: status %d", status)
	}

	rc.mu.Lock()
	rc.ctrlConn = conn
	rc.mu.Unlock()

	rc.logger.Info("registered with relay", "relay", rc.relayAddr)

	// Context cancellation closes the connection.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	// Send keepalive bytes every 30s to prevent the relay's 90s read timeout.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				rc.mu.Lock()
				c := rc.ctrlConn
				rc.mu.Unlock()
				if c == nil {
					return
				}
				c.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Write([]byte{0x00}); err != nil {
					return // connection dead, read loop will handle cleanup
				}
				c.SetWriteDeadline(time.Time{})
			case <-ctx.Done():
				return
			}
		}
	}()

	buf := make([]byte, 1)
	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, err := conn.Read(buf)
		if err != nil {
			rc.mu.Lock()
			rc.ctrlConn = nil
			rc.mu.Unlock()
			return fmt.Errorf("control connection read: %w", err)
		}

		if buf[0] == relayNotifyIncoming {
			// Worker is waiting — open a data connection.
			go rc.handleIncoming(onConnection)
		}
		// Other bytes are keepalive echoes, ignore.
	}
}

func (rc *RelayClient) handleIncoming(onConnection func(net.Conn)) {
	conn, err := net.DialTimeout("tcp", rc.relayAddr, 10*time.Second)
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
func DialRelay(relayAddr string, swarmCode string, identity *NodeIdentity) (net.Conn, error) {
	hash := sha256.Sum256([]byte(swarmCode))

	conn, err := net.DialTimeout("tcp", relayAddr, 10*time.Second)
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

// SelfTestReachability checks if this node is reachable at the given address
// by performing a TCP dial to itself via its public IP.
func SelfTestReachability(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
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
