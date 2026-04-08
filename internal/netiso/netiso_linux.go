//go:build linux

package netiso

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vishvananda/netlink"
)

// NetIso manages per-agent network isolation on Linux.
type NetIso struct {
	impl *netIsoState
}

// netIsoState holds the internal state for network isolation.
type netIsoState struct {
	mu     sync.Mutex
	agents map[uint32]*agentState // agentID → state
	fw     *firewall              // nftables management
	dns    *DNSForwarder          // DNS forwarder
	logger *slog.Logger
}

// agentState tracks the network resources for a single agent.
type agentState struct {
	network AgentNetwork
	hostIF  string // host-side veth interface name
}

// Probe checks whether network isolation is available (CAP_NET_ADMIN).
// Returns nil if available, or an error describing why not.
func Probe() error {
	_, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("CAP_NET_ADMIN not available: %w", err)
	}
	return nil
}

// New creates a new NetIso instance. It initializes nftables rules and
// enables IP forwarding. Returns an error if CAP_NET_ADMIN is missing.
func New(logger *slog.Logger) (*NetIso, error) {
	if err := enableIPForwarding(); err != nil {
		return nil, fmt.Errorf("enabling IP forwarding: %w", err)
	}

	fw, err := newFirewall(logger)
	if err != nil {
		return nil, fmt.Errorf("initializing nftables: %w", err)
	}

	dns := newDNSForwarder(fw, logger)

	return &NetIso{
		impl: &netIsoState{
			agents: make(map[uint32]*agentState),
			fw:     fw,
			dns:    dns,
			logger: logger,
		},
	}, nil
}

// Setup creates a network namespace, veth pair, nftables rules, and DNS
// listener for an agent. Must be called after the worker process is forked
// (with CLONE_NEWNET) but before it signals readiness.
func (n *NetIso) Setup(ctx context.Context, agent AgentNetwork) error {
	impl := n.impl
	impl.mu.Lock()
	defer impl.mu.Unlock()

	prefix := agent.SessionPrefix()
	impl.logger.Info("setting up network isolation",
		"agent_id", agent.AgentID,
		"session", prefix,
		"pid", agent.PID,
		"egress", agent.Egress,
	)

	// 1. Create veth pair and configure networking.
	hostIF, err := setupVeth(agent)
	if err != nil {
		return fmt.Errorf("veth setup: %w", err)
	}

	// 2. Bind-mount per-agent /etc/resolv.conf and /etc/hosts.
	if err := setupMounts(agent); err != nil {
		cleanupVeth(hostIF)
		return fmt.Errorf("mount setup: %w", err)
	}

	// 3. Configure nftables rules.
	isWildcard := len(agent.Egress) == 1 && agent.Egress[0] == "*"
	if err := impl.fw.setupAgent(agent, isWildcard); err != nil {
		cleanupVeth(hostIF)
		return fmt.Errorf("nftables setup: %w", err)
	}

	// 4. Register with DNS forwarder and start listener.
	if err := impl.dns.RegisterAgent(agent); err != nil {
		impl.fw.teardownAgent(agent.SessionPrefix())
		cleanupVeth(hostIF)
		return fmt.Errorf("DNS forwarder setup: %w", err)
	}

	impl.agents[agent.AgentID] = &agentState{
		network: agent,
		hostIF:  hostIF,
	}

	impl.logger.Info("network isolation ready",
		"agent_id", agent.AgentID,
		"gateway", agent.GatewayIP(),
		"agent_ip", agent.AgentIP(),
	)
	return nil
}

// Teardown removes network isolation for an agent. Safe to call multiple
// times (idempotent). The network namespace itself is destroyed when the
// worker process exits.
func (n *NetIso) Teardown(sessionID string) error {
	impl := n.impl
	impl.mu.Lock()
	defer impl.mu.Unlock()

	var agentID uint32
	var state *agentState
	for id, s := range impl.agents {
		if s.network.SessionID == sessionID {
			agentID = id
			state = s
			break
		}
	}
	if state == nil {
		return nil // already torn down or never set up
	}

	prefix := state.network.SessionPrefix()
	impl.logger.Info("tearing down network isolation",
		"agent_id", agentID,
		"session", prefix,
	)

	impl.dns.UnregisterAgent(agentID)

	if err := impl.fw.teardownAgent(prefix); err != nil {
		impl.logger.Warn("nftables teardown error", "error", err, "session", prefix)
	}

	cleanupVeth(state.hostIF)
	delete(impl.agents, agentID)
	return nil
}

// Close tears down all active agents, shuts down the DNS forwarder,
// and cleans up the nftables table.
func (n *NetIso) Close() error {
	impl := n.impl
	impl.mu.Lock()
	defer impl.mu.Unlock()

	// Sweep any agents that weren't explicitly torn down.
	for id, state := range impl.agents {
		impl.dns.UnregisterAgent(id)
		_ = impl.fw.teardownAgent(state.network.SessionPrefix())
		cleanupVeth(state.hostIF)
	}
	impl.agents = make(map[uint32]*agentState)

	impl.dns.Close()
	impl.fw.close()

	return nil
}
