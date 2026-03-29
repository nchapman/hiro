package cluster

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// DiscoveryConfig configures the tracker discovery client.
type DiscoveryConfig struct {
	TrackerURL string // e.g. "https://discover.hellohiro.ai"
	SwarmCode  string // raw swarm code (hashed before sending)
	Role       string // "leader" or "worker"
	GRPCPort   int    // cluster gRPC port to announce
	Identity   *NodeIdentity
	NodeName   string
	Logger     *slog.Logger
}

// DiscoveryClient periodically announces to the tracker and caches discovered peers.
type DiscoveryClient struct {
	trackerURL string
	swarmHash  string // hex(sha256(swarm_code))
	role       string
	grpcPort   int
	identity   *NodeIdentity
	nodeName   string
	logger     *slog.Logger
	client     *http.Client

	mu    sync.RWMutex
	peers []DiscoveredPeer
}

// DiscoveredPeer is a peer returned by the tracker.
type DiscoveredPeer struct {
	NodeID    string
	PublicKey string
	Role      string
	Endpoint  string
	GRPCPort  int
	LastSeen  time.Time
}

// announceRequest matches the tracker's API contract.
type announceRequest struct {
	SwarmHash  string            `json:"swarm_hash"`
	PublicKey  string            `json:"public_key"`
	Signature  string            `json:"signature"`
	Timestamp  int64             `json:"timestamp"`
	Role       string            `json:"role"`
	GRPCPort   int               `json:"grpc_port"`
	WGPubKey   string            `json:"wg_pubkey,omitempty"`
	WGEndpoint string            `json:"wg_endpoint,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// signedMessage constructs the canonical message for signing.
// Must match the tracker's signedMessage() exactly.
func (req *announceRequest) signedMessage() []byte {
	msg := req.SwarmHash + "\n" +
		strconv.FormatInt(req.Timestamp, 10) + "\n" +
		req.Role + "\n" +
		strconv.Itoa(req.GRPCPort) + "\n" +
		req.WGPubKey + "\n" +
		req.WGEndpoint
	if len(req.Meta) > 0 {
		keys := make([]string, 0, len(req.Meta))
		for k := range req.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			msg += "\n" + k + "=" + req.Meta[k]
		}
	}
	return []byte(msg)
}

type announceResponse struct {
	YourIP string         `json:"your_ip"`
	NodeID string         `json:"node_id"`
	Peers  []announceJSON `json:"peers"`
}

type announceJSON struct {
	NodeID    string `json:"node_id"`
	PublicKey string `json:"public_key"`
	Role      string `json:"role"`
	Endpoint  string `json:"endpoint"`
	GRPCPort  int    `json:"grpc_port"`
	LastSeen  string `json:"last_seen"`
}

// NewDiscoveryClient creates a discovery client. Call Run() to start announcing.
func NewDiscoveryClient(cfg DiscoveryConfig) *DiscoveryClient {
	hash := sha256.Sum256([]byte(cfg.SwarmCode))
	return &DiscoveryClient{
		trackerURL: cfg.TrackerURL,
		swarmHash:  hex.EncodeToString(hash[:]),
		role:       cfg.Role,
		grpcPort:   cfg.GRPCPort,
		identity:   cfg.Identity,
		nodeName:   cfg.NodeName,
		logger:     cfg.Logger,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Run announces periodically until ctx is cancelled. It blocks.
func (d *DiscoveryClient) Run(ctx context.Context) {
	// Announce immediately on start, then every 30s.
	d.announce(ctx)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.announce(ctx)
		}
	}
}

// Peers returns the latest discovered peers.
func (d *DiscoveryClient) Peers() []DiscoveredPeer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]DiscoveredPeer, len(d.peers))
	copy(out, d.peers)
	return out
}

// LeaderAddr returns the gRPC address of the most recently seen leader,
// or empty string if none found. Logs a warning if multiple leaders are present.
func (d *DiscoveryClient) LeaderAddr() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var best *DiscoveredPeer
	leaderCount := 0
	for i := range d.peers {
		p := &d.peers[i]
		if p.Role != "leader" {
			continue
		}
		leaderCount++
		if best == nil || p.LastSeen.After(best.LastSeen) {
			best = p
		}
	}
	if best == nil {
		return ""
	}
	if leaderCount > 1 {
		d.logger.Warn("multiple leaders discovered, using most recent", "count", leaderCount)
	}
	return fmt.Sprintf("%s:%d", best.Endpoint, best.GRPCPort)
}

// Announce triggers a single announce call to the tracker, updating the peer cache.
func (d *DiscoveryClient) Announce(ctx context.Context) {
	d.announce(ctx)
}

// WaitForLeader blocks until a leader is discovered or ctx is cancelled.
// Each poll iteration announces to the tracker to get fresh data.
func (d *DiscoveryClient) WaitForLeader(ctx context.Context) (string, error) {
	// Check immediately (announce already ran at the start of Run).
	if addr := d.LeaderAddr(); addr != "" {
		return addr, nil
	}

	// Re-announce every 5 seconds until a leader appears.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("context cancelled while waiting for leader: %w", ctx.Err())
		case <-ticker.C:
			d.announce(ctx)
			if addr := d.LeaderAddr(); addr != "" {
				return addr, nil
			}
		}
	}
}

func (d *DiscoveryClient) announce(ctx context.Context) {
	req := &announceRequest{
		SwarmHash: d.swarmHash,
		PublicKey: base64.StdEncoding.EncodeToString(d.identity.PublicKey),
		Timestamp: time.Now().Unix(),
		Role:      d.role,
		GRPCPort:  d.grpcPort,
		Meta: map[string]string{
			"node_name": d.nodeName,
		},
	}

	// Sign the announcement.
	sig := ed25519.Sign(d.identity.PrivateKey, req.signedMessage())
	req.Signature = base64.StdEncoding.EncodeToString(sig)

	body, err := json.Marshal(req)
	if err != nil {
		d.logger.Error("failed to marshal announce request", "error", err)
		return
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.trackerURL+"/announce", bytes.NewReader(body))
	if err != nil {
		d.logger.Error("failed to create announce request", "error", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(httpReq)
	if err != nil {
		d.logger.Warn("tracker announce failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		d.logger.Warn("tracker announce returned error",
			"status", resp.StatusCode,
			"body", string(respBody),
		)
		return
	}

	var announceResp announceResponse
	if err := json.NewDecoder(resp.Body).Decode(&announceResp); err != nil {
		d.logger.Warn("failed to decode tracker response", "error", err)
		return
	}

	// Update cached peers.
	peers := make([]DiscoveredPeer, 0, len(announceResp.Peers))
	for _, p := range announceResp.Peers {
		lastSeen, err := time.Parse(time.RFC3339, p.LastSeen)
		if err != nil && p.LastSeen != "" {
			d.logger.Debug("failed to parse peer LastSeen", "value", p.LastSeen, "error", err)
		}
		peers = append(peers, DiscoveredPeer{
			NodeID:    p.NodeID,
			PublicKey: p.PublicKey,
			Role:      p.Role,
			Endpoint:  p.Endpoint,
			GRPCPort:  p.GRPCPort,
			LastSeen:  lastSeen,
		})
	}

	d.mu.Lock()
	d.peers = peers
	d.mu.Unlock()

	d.logger.Debug("tracker announce successful",
		"peers", len(peers),
		"your_ip", announceResp.YourIP,
	)
}
