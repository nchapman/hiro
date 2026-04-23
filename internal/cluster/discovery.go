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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// discoveryPollInterval is how often the client re-announces to the tracker.
	discoveryPollInterval = 30 * time.Second

	// announceRetryInterval is how often WaitForLeader re-announces while
	// waiting for a leader to appear.
	announceRetryInterval = 5 * time.Second
)

// DiscoveryConfig configures the tracker discovery client.
type DiscoveryConfig struct {
	TrackerURL     string // e.g. "https://discover.hellohiro.ai"
	SwarmCode      string // raw swarm code (hashed before sending)
	Role           string // "leader" or "worker"
	GRPCPort       int    // cluster gRPC port to announce
	Identity       *NodeIdentity
	TLSFingerprint string // hex SHA-256 of self-signed TLS cert
	NodeName       string
	// AdvertiseAddresses is an optional list of scheme-prefixed URLs
	// ("tcp://host:port") that peers should use to reach this node. When
	// set, the tracker stores these verbatim and drops its observed IP —
	// this is the knob for forcing traffic onto Tailscale or a specific LAN.
	AdvertiseAddresses []string
	Logger             *slog.Logger
}

// DiscoveryClient periodically announces to the tracker and caches discovered peers.
type DiscoveryClient struct {
	trackerURL         string
	swarmHash          string // hex(sha256(swarm_code))
	role               string
	grpcPort           int
	identity           *NodeIdentity
	tlsFingerprint     string
	nodeName           string
	advertiseAddresses []string // validated at construction
	logger             *slog.Logger
	client             *http.Client

	mu       sync.RWMutex
	peers    []DiscoveredPeer
	relayURL string // from tracker response
	yourIP   string // our public IP as seen by tracker
}

// DiscoveredPeer is a peer returned by the tracker. Addresses is always
// populated with at least one scheme-prefixed URL ("tcp://host:port").
type DiscoveredPeer struct {
	NodeID         string
	PublicKey      string
	Role           string
	Addresses      []string
	GRPCPort       int
	TLSFingerprint string
	LastSeen       time.Time
}

// announceRequest matches the tracker's API contract.
type announceRequest struct {
	SwarmHash      string            `json:"swarm_hash"`
	PublicKey      string            `json:"public_key"`
	Signature      string            `json:"signature"`
	Timestamp      int64             `json:"timestamp"`
	Role           string            `json:"role"`
	GRPCPort       int               `json:"grpc_port"`
	WGPubKey       string            `json:"wg_pubkey,omitempty"`
	WGEndpoint     string            `json:"wg_endpoint,omitempty"`
	TLSFingerprint string            `json:"tls_fingerprint,omitempty"`
	Addresses      []string          `json:"addresses,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
}

// signedMessage constructs the canonical message for signing.
// Must match the tracker's signedMessage() exactly.
// Fields are newline-delimited; sanitizeField strips any newlines to prevent
// field injection attacks in the canonical form.
func (req *announceRequest) signedMessage() []byte {
	var b strings.Builder
	b.WriteString(sanitizeField(req.SwarmHash))
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(req.Timestamp, 10))
	b.WriteByte('\n')
	b.WriteString(sanitizeField(req.Role))
	b.WriteByte('\n')
	b.WriteString(strconv.Itoa(req.GRPCPort))
	b.WriteByte('\n')
	b.WriteString(sanitizeField(req.WGPubKey))
	b.WriteByte('\n')
	b.WriteString(sanitizeField(req.WGEndpoint))
	b.WriteByte('\n')
	b.WriteString(sanitizeField(req.TLSFingerprint))
	for _, a := range req.Addresses {
		b.WriteByte('\n')
		b.WriteString(sanitizeField(a))
	}
	if len(req.Meta) > 0 {
		keys := make([]string, 0, len(req.Meta))
		for k := range req.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteByte('\n')
			b.WriteString(sanitizeField(k))
			b.WriteByte('=')
			b.WriteString(sanitizeField(req.Meta[k]))
		}
	}
	return []byte(b.String())
}

// sanitizeField strips control chars (\n, \r, \x00) from a field so they
// can't create ambiguity or splicing in the newline-delimited canonical form.
var fieldSanitizer = strings.NewReplacer("\n", "", "\r", "", "\x00", "")

func sanitizeField(s string) string {
	return fieldSanitizer.Replace(s)
}

type announceResponse struct {
	YourIP   string         `json:"your_ip"`
	NodeID   string         `json:"node_id"`
	Peers    []announceJSON `json:"peers"`
	RelayURL string         `json:"relay_url,omitempty"`
}

type announceJSON struct {
	NodeID         string   `json:"node_id"`
	PublicKey      string   `json:"public_key"`
	Role           string   `json:"role"`
	Addresses      []string `json:"addresses"`
	GRPCPort       int      `json:"grpc_port"`
	TLSFingerprint string   `json:"tls_fingerprint,omitempty"`
	LastSeen       string   `json:"last_seen"`
}

// NewDiscoveryClient creates a discovery client. Call Run() to start announcing.
// Malformed advertise addresses are dropped with a warning log rather than
// propagated as errors — discovery still works, just without the bad entries.
func NewDiscoveryClient(cfg DiscoveryConfig) *DiscoveryClient {
	hash := sha256.Sum256([]byte(cfg.SwarmCode))
	advertise := filterValidAdvertiseAddresses(cfg.AdvertiseAddresses, cfg.Logger)
	if len(advertise) > 0 {
		warnIfNoAddressMatchesLocal(advertise, cfg.Logger)
	}
	return &DiscoveryClient{
		trackerURL:         cfg.TrackerURL,
		swarmHash:          hex.EncodeToString(hash[:]),
		role:               cfg.Role,
		grpcPort:           cfg.GRPCPort,
		identity:           cfg.Identity,
		tlsFingerprint:     cfg.TLSFingerprint,
		nodeName:           cfg.NodeName,
		advertiseAddresses: advertise,
		logger:             cfg.Logger,
		client: &http.Client{
			Timeout: discoveryHTTPTimeout,
		},
	}
}

// Run announces periodically until ctx is cancelled. It blocks.
func (d *DiscoveryClient) Run(ctx context.Context) {
	// Announce immediately on start, then every discoveryPollInterval.
	d.announce(ctx)

	ticker := time.NewTicker(discoveryPollInterval)
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

// Leader returns the most recently seen leader peer, or nil if none found.
// Logs a warning if multiple leaders are present.
func (d *DiscoveryClient) Leader() *DiscoveredPeer {
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
			cp := *p // copy
			best = &cp
		}
	}
	if best == nil {
		return nil
	}
	if leaderCount > 1 {
		d.logger.Warn("multiple leaders discovered, using most recent", "count", leaderCount)
	}
	return best
}

// LeaderAddr returns the first gRPC dial address of the most recently seen
// leader, or empty string if none found. Use LeaderAddresses for the full list.
func (d *DiscoveryClient) LeaderAddr() string {
	addrs := d.LeaderAddresses()
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

// LeaderAddresses returns every dial target (bare "host:port") for the most
// recently seen leader, with the "tcp://" scheme stripped.
func (d *DiscoveryClient) LeaderAddresses() []string {
	leader := d.Leader()
	if leader == nil {
		return nil
	}
	out := make([]string, 0, len(leader.Addresses))
	for _, a := range leader.Addresses {
		if hp := addressHostPort(a); hp != "" {
			out = append(out, hp)
		}
	}
	return out
}

// addressHostPort parses a "tcp://host:port" URL and returns the bare host:port
// suitable for net.Dial. Returns "" if the input isn't a valid scheme URL.
func addressHostPort(a string) string {
	u, err := url.Parse(a)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Host
}

// RelayURL returns the relay server address from the last tracker response.
func (d *DiscoveryClient) RelayURL() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.relayURL
}

// YourIP returns this node's public IP as reported by the tracker.
func (d *DiscoveryClient) YourIP() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.yourIP
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

	// Re-announce every announceRetryInterval until a leader appears.
	ticker := time.NewTicker(announceRetryInterval)
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
	announceResp, err := d.postAnnounce(ctx)
	if err != nil {
		d.logger.Warn("tracker announce failed", "error", err)
		return
	}

	peers := d.parsePeers(announceResp.Peers)

	d.mu.Lock()
	d.peers = peers
	d.relayURL = announceResp.RelayURL
	d.yourIP = announceResp.YourIP
	d.mu.Unlock()

	d.logger.Debug("tracker announce successful",
		"peers", len(peers),
		"your_ip", announceResp.YourIP,
		"relay_url", announceResp.RelayURL,
	)
}

// postAnnounce sends a signed announce request to the tracker and returns the response.
func (d *DiscoveryClient) postAnnounce(ctx context.Context) (*announceResponse, error) {
	req := &announceRequest{
		SwarmHash:      d.swarmHash,
		PublicKey:      base64.StdEncoding.EncodeToString(d.identity.PublicKey),
		Timestamp:      time.Now().Unix(),
		Role:           d.role,
		GRPCPort:       d.grpcPort,
		TLSFingerprint: d.tlsFingerprint,
		Addresses:      d.advertiseAddresses,
		Meta: map[string]string{
			"node_name": d.nodeName,
		},
	}

	sig := ed25519.Sign(d.identity.PrivateKey, req.signedMessage())
	req.Signature = base64.StdEncoding.EncodeToString(sig)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.trackerURL+"/announce", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, discoveryErrorBodyLimit))
		return nil, fmt.Errorf("tracker returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var announceResp announceResponse
	if err := json.NewDecoder(resp.Body).Decode(&announceResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &announceResp, nil
}

// parsePeers converts raw announce JSON peers into typed DiscoveredPeer values.
func (d *DiscoveryClient) parsePeers(raw []announceJSON) []DiscoveredPeer {
	peers := make([]DiscoveredPeer, 0, len(raw))
	for _, p := range raw {
		lastSeen, err := time.Parse(time.RFC3339, p.LastSeen)
		if err != nil && p.LastSeen != "" {
			d.logger.Debug("failed to parse peer LastSeen", "value", p.LastSeen, "error", err)
		}
		peers = append(peers, DiscoveredPeer{
			NodeID:         p.NodeID,
			PublicKey:      p.PublicKey,
			Role:           p.Role,
			Addresses:      p.Addresses,
			GRPCPort:       p.GRPCPort,
			TLSFingerprint: p.TLSFingerprint,
			LastSeen:       lastSeen,
		})
	}
	return peers
}

// SwarmCheckResult is the result of a one-shot swarm validation.
type SwarmCheckResult struct {
	LeaderFound bool   `json:"leader_found"`
	LeaderName  string `json:"leader_name,omitempty"`
}

// CheckSwarm performs a one-shot tracker query to verify a leader exists
// in the given swarm. Uses the node's real identity for the announce.
func CheckSwarm(ctx context.Context, trackerURL, swarmCode string, identity *NodeIdentity, logger *slog.Logger) (*SwarmCheckResult, error) {
	dc := NewDiscoveryClient(DiscoveryConfig{
		TrackerURL: trackerURL,
		SwarmCode:  swarmCode,
		Role:       "worker",
		GRPCPort:   0,
		Identity:   identity,
		NodeName:   "probe",
		Logger:     logger,
	})

	dc.announce(ctx)

	leader := dc.Leader()
	if leader == nil {
		return &SwarmCheckResult{LeaderFound: false}, nil
	}

	name := leader.NodeID
	if len(name) > discoveryNodeIDDisplayLen {
		name = name[:discoveryNodeIDDisplayLen]
	}

	return &SwarmCheckResult{
		LeaderFound: true,
		LeaderName:  name,
	}, nil
}

// Advertise-address constants shared with the tracker's validation.
const (
	MaxAdvertiseAddresses    = 8
	maxAdvertiseAddressBytes = 128
)

var advertiseAddressSchemes = map[string]bool{
	"tcp":  true,
	"tcp4": true,
	"tcp6": true,
}

// ValidateAdvertiseAddress returns an error message if the scheme-prefixed URL
// is malformed or obviously undialable. Host must be an IP literal — hostnames
// are rejected to prevent DNS-based redirect tricks. Kept byte-compatible with
// the tracker's validation so a valid entry here is valid there too.
func ValidateAdvertiseAddress(a string) string {
	if a == "" {
		return "addresses entries must be non-empty"
	}
	if len(a) > maxAdvertiseAddressBytes {
		return fmt.Sprintf("addresses entries must be at most %d characters", maxAdvertiseAddressBytes)
	}
	u, err := url.Parse(a)
	if err != nil || u.Host == "" {
		return "addresses entries must parse as scheme://host:port"
	}
	if !advertiseAddressSchemes[u.Scheme] {
		return "addresses scheme must be tcp, tcp4, or tcp6"
	}
	if u.Path != "" || u.RawQuery != "" || u.User != nil {
		return "addresses must not include path, query, or user info"
	}
	ap, err := netip.ParseAddrPort(u.Host)
	if err != nil {
		return "addresses host must be ip:port (IP literal, not a hostname)"
	}
	if ap.Port() == 0 {
		return "addresses port must be non-zero"
	}
	ip := ap.Addr()
	switch {
	case ip.IsUnspecified():
		return "addresses must not be unspecified (0.0.0.0, ::)"
	case ip.IsLoopback():
		return "addresses must not be loopback"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "addresses must not be link-local"
	case ip.IsMulticast():
		return "addresses must not be multicast"
	}
	return ""
}

// filterValidAdvertiseAddresses returns the subset of addrs that pass
// ValidateAdvertiseAddress, logging a warning for each rejected entry. An
// invalid entry is never sent — discovery still works, it just drops bad rows.
func filterValidAdvertiseAddresses(addrs []string, logger *slog.Logger) []string {
	if len(addrs) == 0 {
		return nil
	}
	if len(addrs) > MaxAdvertiseAddresses {
		if logger != nil {
			logger.Warn("cluster.advertise_addresses exceeds max, truncating",
				"count", len(addrs), "max", MaxAdvertiseAddresses)
		}
		addrs = addrs[:MaxAdvertiseAddresses]
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if msg := ValidateAdvertiseAddress(a); msg != "" {
			if logger != nil {
				logger.Warn("dropping invalid advertise address", "address", a, "reason", msg)
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// warnIfNoAddressMatchesLocal logs a warning when none of the configured
// advertise addresses match a locally-bound interface. This catches typos
// without overriding the user's intent — the addresses are still sent.
// Inside a Docker container, "local" means the container's interfaces, which
// won't include the host's Tailscale IP — so a "no match" warning is normal
// in Docker. The warning exists for bare-metal misconfigurations.
func warnIfNoAddressMatchesLocal(addrs []string, logger *slog.Logger) {
	if logger == nil {
		return
	}
	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	local := make(map[string]bool, len(ifaceAddrs))
	for _, a := range ifaceAddrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if na, ok := netip.AddrFromSlice(ipnet.IP); ok {
				local[na.Unmap().String()] = true
			}
		}
	}
	for _, a := range addrs {
		if u, err := url.Parse(a); err == nil {
			if ap, err := netip.ParseAddrPort(u.Host); err == nil {
				if local[ap.Addr().Unmap().String()] {
					return
				}
			}
		}
	}
	// Logged at Debug (not Info) so we don't spray internal topology into
	// aggregated log sinks by default. Inside Docker the "no match" case is
	// normal — host interfaces aren't visible to the container — so the
	// warning is mostly useful for bare-metal misconfigurations, where the
	// operator can flip log level to investigate.
	logger.Debug("cluster.advertise_addresses: none match a local interface",
		"count", len(addrs))
}
