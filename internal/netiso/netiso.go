// Package netiso implements per-agent network isolation using Linux network
// namespaces, veth pairs, nftables IP sets, and a DNS forwarder.
//
// Each agent worker spawns in its own network namespace (CLONE_NEWNET).
// A veth pair connects it to the control plane, which acts as gateway and
// DNS server. The DNS forwarder resolves allowed domains and populates
// nftables IP sets — filtering is purely at the IP layer, protocol-agnostic.
//
// The full implementation requires Linux (CAP_NET_ADMIN). On other platforms,
// Probe() returns an error and New() is unavailable.
package netiso

import (
	"net"
	"time"
)

// BaseSubnet is the 10.0.0.0/16 range used for agent veth subnets.
// Each agent gets a /30: gateway 10.0.{id}.1, agent 10.0.{id}.2.
const BaseSubnet = "10.0.0.0/16"

// MinTTL is the minimum TTL for nftables IP set entries, preventing
// excessive churn from short-TTL CDN domains.
const MinTTL = 30 * time.Second

// AgentNetwork describes the network configuration for a single agent.
type AgentNetwork struct {
	AgentID   uint32   // UID offset (UID - BaseUID), used for subnet: 10.0.{id}.0/30
	SessionID string   // for nftables chain/set naming (session prefix)
	PID       int      // worker process PID (for netns entry via /proc/{pid}/ns/net)
	Egress    []string // allowed domains; ["*"] = unrestricted
}

const (
	// maxAgentID is the maximum agent ID (limited to fit in a single byte for subnet addressing).
	maxAgentID = 255

	// subnetBase is the first octet of agent subnets (10.0.x.x).
	subnetBase = 10

	// gatewayHostPart is the last octet of the gateway IP (x.x.x.1).
	gatewayHostPart = 1

	// agentHostPart is the last octet of the agent IP (x.x.x.2).
	agentHostPart = 2

	// sessionPrefixLen is the maximum length of a session prefix used for nftables naming.
	sessionPrefixLen = 12
)

// GatewayIP returns the gateway IP for the agent's /30 subnet.
func (a AgentNetwork) GatewayIP() net.IP {
	id := min(a.AgentID, maxAgentID)
	return net.IPv4(subnetBase, 0, byte(id), gatewayHostPart) //nolint:gosec // bounded above
}

// AgentIP returns the agent-side IP for the /30 subnet.
func (a AgentNetwork) AgentIP() net.IP {
	id := min(a.AgentID, maxAgentID)
	return net.IPv4(subnetBase, 0, byte(id), agentHostPart) //nolint:gosec // bounded above
}

// SessionPrefix returns the truncated session ID used for naming.
func (a AgentNetwork) SessionPrefix() string {
	s := a.SessionID
	if len(s) > sessionPrefixLen {
		s = s[:sessionPrefixLen]
	}
	return s
}

// maxIFNameLen is the maximum length of a Linux network interface name.
const maxIFNameLen = 15

// PeerName returns the temporary veth peer name for an agent, used in the host
// namespace before the child renames it to eth0. Must match the naming in setupVeth.
func PeerName(sessionPrefix string) string {
	name := "hp-" + sessionPrefix
	if len(name) > maxIFNameLen {
		name = name[:maxIFNameLen]
	}
	return name
}

// NetIso manages per-agent network isolation. It creates veth pairs,
// configures nftables rules, and runs a DNS forwarder.
// Only available on Linux with CAP_NET_ADMIN.
// The struct is defined in platform-specific files (netiso_linux.go, netiso_other.go).
