//go:build linux

package netiso

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

// buildDNSResponse constructs a dns.Msg with the given answer records.
func buildDNSResponse(answers ...dns.RR) *dns.Msg {
	m := new(dns.Msg)
	m.Answer = answers
	return m
}

func aRecord(name string, ip string, ttl uint32) *dns.A {
	return &dns.A{
		Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Ttl: ttl},
		A:   net.ParseIP(ip),
	}
}

func cnameRecord(name, target string, ttl uint32) *dns.CNAME {
	return &dns.CNAME{
		Hdr:    dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeCNAME, Ttl: ttl},
		Target: dns.Fqdn(target),
	}
}

func TestExtractIPs_NoCNAME(t *testing.T) {
	resp := buildDNSResponse(
		aRecord("github.com", "1.2.3.4", 300),
		aRecord("github.com", "5.6.7.8", 300),
	)
	ips, ttl := extractIPs(resp, []string{"github.com"})
	if len(ips) != 2 {
		t.Fatalf("expected 2 IPs, got %d", len(ips))
	}
	if ttl != 300 {
		t.Errorf("expected TTL 300, got %d", ttl)
	}
}

func TestExtractIPs_ValidCNAMEChain(t *testing.T) {
	// github.com CNAME cdn.github.com, cdn.github.com A 1.2.3.4
	// Both are in the allowlist via *.github.com.
	resp := buildDNSResponse(
		cnameRecord("github.com", "cdn.github.com", 300),
		aRecord("cdn.github.com", "1.2.3.4", 60),
	)
	ips, _ := extractIPs(resp, []string{"github.com", "*.github.com"})
	if len(ips) != 1 {
		t.Fatalf("expected 1 IP for valid CNAME chain, got %d", len(ips))
	}
}

func TestExtractIPs_CNAMEOutsideAllowlist_RejectsAll(t *testing.T) {
	// github.com CNAME attacker.com, attacker.com A 1.2.3.4
	// attacker.com is NOT in the allowlist — all IPs should be rejected.
	resp := buildDNSResponse(
		cnameRecord("github.com", "attacker.com", 300),
		aRecord("attacker.com", "1.2.3.4", 60),
	)
	ips, _ := extractIPs(resp, []string{"github.com"})
	if len(ips) != 0 {
		t.Errorf("expected 0 IPs when CNAME exits allowlist, got %d: %v", len(ips), ips)
	}
}

func TestExtractIPs_DeepCNAMEChainPartiallyOutside(t *testing.T) {
	// a.github.com CNAME b.github.com CNAME evil.com, evil.com A 1.2.3.4
	// The last CNAME exits the allowlist.
	resp := buildDNSResponse(
		cnameRecord("a.github.com", "b.github.com", 300),
		cnameRecord("b.github.com", "evil.com", 300),
		aRecord("evil.com", "1.2.3.4", 60),
	)
	ips, _ := extractIPs(resp, []string{"*.github.com"})
	if len(ips) != 0 {
		t.Errorf("expected 0 IPs when deep CNAME exits allowlist, got %d", len(ips))
	}
}

func TestExtractIPs_WildcardEgressSkipsCNAMECheck(t *testing.T) {
	// With wildcard egress ["*"], CNAME targets should not be validated.
	resp := buildDNSResponse(
		cnameRecord("github.com", "anything.evil.com", 300),
		aRecord("anything.evil.com", "1.2.3.4", 60),
	)
	ips, _ := extractIPs(resp, []string{"*"})
	if len(ips) != 1 {
		t.Errorf("expected 1 IP with wildcard egress, got %d", len(ips))
	}
}

func TestExtractIPs_EmptyResponse(t *testing.T) {
	resp := buildDNSResponse()
	ips, _ := extractIPs(resp, []string{"github.com"})
	if len(ips) != 0 {
		t.Errorf("expected 0 IPs for empty response, got %d", len(ips))
	}
}
