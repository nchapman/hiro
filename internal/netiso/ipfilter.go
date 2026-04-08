package netiso

import "net"

// privateNets are the IP ranges that must be filtered from DNS responses
// before adding to nftables sets. This prevents DNS rebinding attacks
// where an allowed domain resolves to a private IP.
//
//nolint:mnd // IP addresses and CIDR masks are self-documenting
var privateNets = []net.IPNet{
	// RFC 1918.
	{IP: net.IPv4(10, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(172, 16, 0, 0), Mask: net.CIDRMask(12, 32)},
	{IP: net.IPv4(192, 168, 0, 0), Mask: net.CIDRMask(16, 32)},
	// Loopback.
	{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	// Link-local.
	{IP: net.IPv4(169, 254, 0, 0), Mask: net.CIDRMask(16, 32)},
	// Multicast.
	{IP: net.IPv4(224, 0, 0, 0), Mask: net.CIDRMask(4, 32)},
}

// isPrivateIP reports whether the IP is in a private/reserved range
// that should not be added to agent nftables sets.
func isPrivateIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return true // treat IPv6 as private (we disable IPv6 in agent namespaces)
	}
	if ip4.IsUnspecified() {
		return true
	}
	for _, n := range privateNets {
		if n.Contains(ip4) {
			return true
		}
	}
	return false
}

// filterPrivateIPs returns only public IPs from the input slice.
func filterPrivateIPs(ips []net.IP) []net.IP {
	var public []net.IP
	for _, ip := range ips {
		if !isPrivateIP(ip) {
			public = append(public, ip)
		}
	}
	return public
}
