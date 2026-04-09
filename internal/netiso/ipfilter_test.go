package netiso

import (
	"net"
	"testing"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		// Private ranges.
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},

		// Loopback.
		{"127.0.0.1", true},
		{"127.0.0.2", true},

		// Link-local.
		{"169.254.1.1", true},

		// Multicast.
		{"224.0.0.1", true},
		{"239.255.255.255", true},

		// Unspecified.
		{"0.0.0.0", true},

		// Public.
		{"8.8.8.8", false},
		{"140.82.121.3", false}, // github.com
		{"1.1.1.1", false},
		{"151.101.1.194", false},

		// Non-private ranges.
		{"172.32.0.1", false},  // just outside 172.16/12
		{"11.0.0.1", false},    // just outside 10/8
		{"192.169.0.1", false}, // just outside 192.168/16
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		got := isPrivateIP(ip)
		if got != tt.want {
			t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

func TestFilterPrivateIPs(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("8.8.8.8"),
		net.ParseIP("10.0.0.1"),
		net.ParseIP("140.82.121.3"),
		net.ParseIP("127.0.0.1"),
		net.ParseIP("1.1.1.1"),
	}
	got := filterPrivateIPs(ips)
	if len(got) != 3 {
		t.Fatalf("filterPrivateIPs returned %d IPs, want 3", len(got))
	}
	want := []string{"8.8.8.8", "140.82.121.3", "1.1.1.1"}
	for i, ip := range got {
		if ip.String() != want[i] {
			t.Errorf("got[%d] = %s, want %s", i, ip, want[i])
		}
	}
}
