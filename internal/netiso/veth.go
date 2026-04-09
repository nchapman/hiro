//go:build linux

package netiso

import (
	"fmt"
	"net"
	"os"

	"github.com/vishvananda/netlink"
)

// enableIPForwarding enables IPv4 forwarding on the host (container).
// If the sysctl is read-only (common in containers without --privileged),
// it checks whether forwarding is already enabled. Docker compose files
// should set `sysctls: [net.ipv4.ip_forward=1]` as a fallback.
func enableIPForwarding() error {
	err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)
	if err == nil {
		return nil
	}
	// Check if already enabled (read-only procfs in containers).
	data, readErr := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if readErr != nil {
		return fmt.Errorf("cannot enable or read ip_forward: write=%w, read=%w", err, readErr)
	}
	if len(data) > 0 && data[0] == '1' {
		return nil // already enabled
	}
	return fmt.Errorf("ip_forward is disabled and read-only: %w (set sysctls: [net.ipv4.ip_forward=1] in docker-compose)", err)
}

// setupVeth creates a veth pair, moves the agent-side peer into the worker's
// network namespace, and configures the host side (IP address, bring up).
// The agent side is NOT configured here — the child process self-configures
// from inside its user namespace.
// Returns the host-side interface name and the peer name (for the child to rename to eth0).
func setupVeth(agent AgentNetwork) (hostIF, peerName string, err error) {
	prefix := agent.SessionPrefix()
	hostName := "hiro-" + prefix
	if len(hostName) > maxIFNameLen {
		hostName = hostName[:maxIFNameLen]
	}
	// Use a temporary peer name in the host namespace, then the child
	// renames it to eth0 inside its namespace.
	peerName = PeerName(prefix)

	gwIP := agent.GatewayIP()
	mask := net.CIDRMask(30, 32)

	// Create veth pair.
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: hostName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return "", "", fmt.Errorf("creating veth pair: %w", err)
	}

	// On failure, clean up the host-side veth (which also removes the peer).
	defer func() {
		if err != nil {
			cleanupVeth(hostName)
		}
	}()

	// Move peer into agent's network namespace.
	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		return "", "", fmt.Errorf("finding peer %s: %w", peerName, err)
	}
	if err := netlink.LinkSetNsPid(peer, agent.PID); err != nil {
		return "", "", fmt.Errorf("moving %s to pid %d netns: %w", peerName, agent.PID, err)
	}

	// Configure host side: assign gateway IP and bring up.
	hostLink, err := netlink.LinkByName(hostName)
	if err != nil {
		return "", "", fmt.Errorf("finding host link %s: %w", hostName, err)
	}
	if err := netlink.AddrAdd(hostLink, &netlink.Addr{
		IPNet: &net.IPNet{IP: gwIP, Mask: mask},
	}); err != nil {
		return "", "", fmt.Errorf("adding address to %s: %w", hostName, err)
	}
	if err := netlink.LinkSetUp(hostLink); err != nil {
		return "", "", fmt.Errorf("bringing up %s: %w", hostName, err)
	}

	return hostName, peerName, nil
}

// cleanupVeth removes the host-side veth interface (which also removes the peer).
func cleanupVeth(hostIF string) {
	if hostIF == "" {
		return
	}
	link, err := netlink.LinkByName(hostIF)
	if err != nil {
		return // already gone
	}
	_ = netlink.LinkDel(link)
}
