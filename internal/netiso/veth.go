//go:build linux

package netiso

import (
	"fmt"
	"net"
	"os"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
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

// setupVeth creates a veth pair, moves the agent end into the worker's
// network namespace, and configures addresses and routes.
// Returns the host-side interface name.
func setupVeth(agent AgentNetwork) (hostIF string, err error) {
	prefix := agent.SessionPrefix()
	hostName := "hiro-" + prefix
	// Linux interface names are limited to 15 chars.
	if len(hostName) > 15 {
		hostName = hostName[:15]
	}
	// Use a temporary peer name in the host namespace, then rename to eth0
	// after moving into the agent's namespace (avoids conflict with host eth0).
	peerName := "hp-" + prefix
	if len(peerName) > 15 {
		peerName = peerName[:15]
	}

	gwIP := agent.GatewayIP()
	agentIP := agent.AgentIP()
	mask := net.CIDRMask(30, 32)

	// Create veth pair.
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: hostName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return "", fmt.Errorf("creating veth pair: %w", err)
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
		return "", fmt.Errorf("finding peer %s: %w", peerName, err)
	}
	if err := netlink.LinkSetNsPid(peer, agent.PID); err != nil {
		return "", fmt.Errorf("moving %s to pid %d netns: %w", peerName, agent.PID, err)
	}

	// Configure host side: assign gateway IP and bring up.
	hostLink, err := netlink.LinkByName(hostName)
	if err != nil {
		return "", fmt.Errorf("finding host link %s: %w", hostName, err)
	}
	if err := netlink.AddrAdd(hostLink, &netlink.Addr{
		IPNet: &net.IPNet{IP: gwIP, Mask: mask},
	}); err != nil {
		return "", fmt.Errorf("adding address to %s: %w", hostName, err)
	}
	if err := netlink.LinkSetUp(hostLink); err != nil {
		return "", fmt.Errorf("bringing up %s: %w", hostName, err)
	}

	// Configure agent side: enter namespace, rename to eth0, assign IP, set default route, disable IPv6.
	if err := configureAgentNS(agent.PID, peerName, agentIP, gwIP, mask); err != nil {
		return "", fmt.Errorf("configuring agent namespace: %w", err)
	}

	return hostName, nil
}

// configureAgentNS enters the agent's network namespace, renames the peer
// interface to eth0, configures its address/route, and disables IPv6.
func configureAgentNS(pid int, peerName string, agentIP, gwIP net.IP, mask net.IPMask) error {
	// Must lock OS thread to safely switch namespaces.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Save current namespace.
	origNS, err := netns.Get()
	if err != nil {
		return fmt.Errorf("getting current netns: %w", err)
	}
	defer origNS.Close()

	// Enter agent's network namespace.
	agentNS, err := netns.GetFromPid(pid)
	if err != nil {
		return fmt.Errorf("getting netns for pid %d: %w", pid, err)
	}
	defer agentNS.Close()

	if err := netns.Set(agentNS); err != nil {
		return fmt.Errorf("entering agent netns: %w", err)
	}
	defer netns.Set(origNS) //nolint:errcheck // best-effort restore

	// Bring up loopback.
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("finding lo: %w", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return fmt.Errorf("bringing up lo: %w", err)
	}

	// Rename the peer interface to eth0.
	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		return fmt.Errorf("finding peer %s in agent ns: %w", peerName, err)
	}
	if err := netlink.LinkSetName(peer, "eth0"); err != nil {
		return fmt.Errorf("renaming %s to eth0: %w", peerName, err)
	}

	// Configure eth0.
	eth0, err := netlink.LinkByName("eth0")
	if err != nil {
		return fmt.Errorf("finding eth0: %w", err)
	}
	if err := netlink.AddrAdd(eth0, &netlink.Addr{
		IPNet: &net.IPNet{IP: agentIP, Mask: mask},
	}); err != nil {
		return fmt.Errorf("adding address to eth0: %w", err)
	}
	if err := netlink.LinkSetUp(eth0); err != nil {
		return fmt.Errorf("bringing up eth0: %w", err)
	}

	// Add default route via gateway.
	if err := netlink.RouteAdd(&netlink.Route{
		Gw: gwIP,
	}); err != nil {
		return fmt.Errorf("adding default route: %w", err)
	}

	// Disable IPv6 in agent namespace.
	_ = os.WriteFile("/proc/sys/net/ipv6/conf/eth0/disable_ipv6", []byte("1"), 0o644)
	_ = os.WriteFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", []byte("1"), 0o644)

	return nil
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
