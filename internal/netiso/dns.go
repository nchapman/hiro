//go:build linux

package netiso

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	// maxCNAMEDepth limits CNAME chain traversal (matches Go's resolver).
	maxCNAMEDepth = 8

	// defaultUpstreamDNS is Docker's embedded DNS resolver.
	defaultUpstreamDNS = "127.0.0.11:53"
)

// DNSForwarder resolves allowed domains for agents and populates nftables
// IP sets with the resolved addresses.
type DNSForwarder struct {
	mu       sync.RWMutex
	agents   map[uint32]*agentDNS // agentID → DNS config
	fw       *firewall
	upstream string // upstream DNS server address
	logger   *slog.Logger
	client   *dns.Client
}

// agentDNS holds per-agent DNS configuration and servers.
type agentDNS struct {
	agentID       uint32
	sessionPrefix string
	egress        []string
	wildcard      bool         // egress == ["*"]
	servers       []*dns.Server // UDP + TCP listeners for this agent
}

// newDNSForwarder creates a DNS forwarder that uses the given firewall
// for IP set population.
func newDNSForwarder(fw *firewall, logger *slog.Logger) *DNSForwarder {
	upstream := detectUpstreamDNS()
	logger.Info("DNS forwarder using upstream", "upstream", upstream)
	return &DNSForwarder{
		agents:   make(map[uint32]*agentDNS),
		fw:       fw,
		upstream: upstream,
		logger:   logger.With("component", "dns-forwarder"),
		client: &dns.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// detectUpstreamDNS finds a working upstream DNS resolver.
// Prefers Docker's embedded DNS (127.0.0.11) which is always at a loopback
// address. Skips agent-subnet addresses (10.0.x.x) to avoid routing through
// an agent's own gateway. Falls back to Docker's embedded DNS.
func detectUpstreamDNS() string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "nameserver ") {
				ns := strings.TrimSpace(strings.TrimPrefix(line, "nameserver"))
				ip := net.ParseIP(ns)
				if ip == nil {
					continue
				}
				// Prefer loopback (Docker's embedded DNS is 127.0.0.11).
				// Skip private addresses that may be container gateway IPs.
				if ip.IsLoopback() {
					return ns + ":53"
				}
			}
		}
	}
	return defaultUpstreamDNS
}

// RegisterAgent starts DNS listeners on the agent's gateway IP.
func (d *DNSForwarder) RegisterAgent(agent AgentNetwork) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	isWildcard := len(agent.Egress) == 1 && agent.Egress[0] == "*"
	gwIP := agent.GatewayIP().String()
	prefix := agent.SessionPrefix()
	listenAddr := gwIP + ":53"
	handler := d.handlerFor(agent.AgentID)

	udpServer := &dns.Server{
		Addr:    listenAddr,
		Net:     "udp",
		Handler: handler,
	}
	tcpServer := &dns.Server{
		Addr:    listenAddr,
		Net:     "tcp",
		Handler: handler,
	}

	go func() {
		if err := udpServer.ListenAndServe(); err != nil {
			d.logger.Debug("DNS UDP server stopped", "addr", listenAddr, "error", err)
		}
	}()
	go func() {
		if err := tcpServer.ListenAndServe(); err != nil {
			d.logger.Debug("DNS TCP server stopped", "addr", listenAddr, "error", err)
		}
	}()

	d.agents[agent.AgentID] = &agentDNS{
		agentID:       agent.AgentID,
		sessionPrefix: prefix,
		egress:        agent.Egress,
		wildcard:      isWildcard,
		servers:       []*dns.Server{udpServer, tcpServer},
	}

	d.logger.Info("DNS forwarder registered",
		"agent_id", agent.AgentID,
		"listen", listenAddr,
		"egress", agent.Egress,
	)
	return nil
}

// UnregisterAgent stops DNS listeners for the given agent.
func (d *DNSForwarder) UnregisterAgent(agentID uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ad, ok := d.agents[agentID]
	if !ok {
		return
	}

	// Shut down this agent's DNS servers.
	for _, srv := range ad.servers {
		srv.Shutdown() //nolint:errcheck
	}

	delete(d.agents, agentID)

	d.logger.Info("DNS forwarder unregistered",
		"agent_id", agentID,
		"session", ad.sessionPrefix,
	)
}

// Close shuts down all DNS listeners.
func (d *DNSForwarder) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, ad := range d.agents {
		for _, srv := range ad.servers {
			srv.Shutdown() //nolint:errcheck
		}
	}
	d.agents = make(map[uint32]*agentDNS)
}

// handlerFor returns a dns.Handler for the given agent.
func (d *DNSForwarder) handlerFor(agentID uint32) dns.Handler {
	return dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		d.handleQuery(w, r, agentID)
	})
}

// handleQuery processes a single DNS query for an agent.
func (d *DNSForwarder) handleQuery(w dns.ResponseWriter, r *dns.Msg, agentID uint32) {
	if len(r.Question) == 0 {
		dns.HandleFailed(w, r)
		return
	}

	q := r.Question[0]
	qname := strings.TrimSuffix(q.Name, ".")

	d.mu.RLock()
	ad, ok := d.agents[agentID]
	d.mu.RUnlock()
	if !ok {
		dns.HandleFailed(w, r)
		return
	}

	// Check domain against allowlist.
	if !ad.wildcard && !MatchDomain(qname, ad.egress) {
		d.logger.Info("DNS query denied",
			"agent_id", agentID,
			"domain", qname,
		)
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeNameError) // NXDOMAIN
		w.WriteMsg(m)                      //nolint:errcheck
		return
	}

	// Forward to upstream DNS.
	resp, _, err := d.client.Exchange(r, d.upstream)
	if err != nil {
		d.logger.Warn("upstream DNS error",
			"agent_id", agentID,
			"domain", qname,
			"error", err,
		)
		dns.HandleFailed(w, r)
		return
	}

	// Only process A/AAAA queries for IP set population.
	if q.Qtype == dns.TypeA || q.Qtype == dns.TypeAAAA {
		ips, minTTL := extractIPs(resp, ad.egress)
		publicIPs := filterPrivateIPs(ips)

		if len(publicIPs) > 0 {
			ttl := time.Duration(minTTL) * time.Second
			if ttl < MinTTL {
				ttl = MinTTL
			}

			// Synchronously write IPs to nftables set BEFORE returning response.
			if err := d.fw.addIPs(ad.sessionPrefix, publicIPs, ttl); err != nil {
				d.logger.Error("failed to add IPs to nftables set",
					"agent_id", agentID,
					"domain", qname,
					"error", err,
				)
				// Return SERVFAIL — agent will retry.
				dns.HandleFailed(w, r)
				return
			}

			d.logger.Info("DNS query allowed",
				"agent_id", agentID,
				"domain", qname,
				"ips", fmtIPs(publicIPs),
				"ttl", ttl,
			)
		}
	}

	w.WriteMsg(resp) //nolint:errcheck
}

// extractIPs collects all A/AAAA record IPs from a DNS response, validating
// CNAME targets against the egress allowlist. If a CNAME points to a domain
// outside the allowlist, the entire response is rejected (no IPs returned)
// to prevent allowlist bypass via CNAME redirection.
func extractIPs(resp *dns.Msg, egress []string) (ips []net.IP, minTTL uint32) {
	minTTL = 3600 // default 1h
	wildcard := len(egress) == 1 && egress[0] == "*"

	// First pass: validate all CNAME targets against the allowlist.
	// A CNAME chain that exits the allowed domain set could redirect
	// traffic to attacker-controlled IPs.
	if !wildcard {
		for _, rr := range resp.Answer {
			if cname, ok := rr.(*dns.CNAME); ok {
				target := strings.TrimSuffix(cname.Target, ".")
				if !MatchDomain(target, egress) {
					return nil, 0 // CNAME target outside allowlist — reject
				}
			}
		}
	}

	// Second pass: collect IPs (all CNAME targets validated above).
	for _, rr := range resp.Answer {
		switch v := rr.(type) {
		case *dns.A:
			ips = append(ips, v.A)
			if v.Hdr.Ttl < minTTL {
				minTTL = v.Hdr.Ttl
			}
		case *dns.AAAA:
			ips = append(ips, v.AAAA)
			if v.Hdr.Ttl < minTTL {
				minTTL = v.Hdr.Ttl
			}
		}
	}
	return ips, minTTL
}

// fmtIPs formats a slice of IPs for logging.
func fmtIPs(ips []net.IP) string {
	strs := make([]string, len(ips))
	for i, ip := range ips {
		strs[i] = ip.String()
	}
	return fmt.Sprintf("[%s]", strings.Join(strs, ", "))
}
