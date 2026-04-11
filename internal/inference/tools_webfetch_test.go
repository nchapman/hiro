package inference

import (
	"context"
	"net"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"10.0.0.1",        // private
		"172.16.0.1",      // private
		"192.168.1.1",     // private
		"169.254.1.1",     // link-local
		"0.0.0.0",         // unspecified
		"fe80::1",         // link-local v6
		"ff02::1",         // link-local multicast
		"ff0e::1",         // global multicast
		"100.64.0.1",      // CGNAT (RFC 6598)
		"100.100.100.200", // Alibaba Cloud metadata
	}
	for _, addr := range blocked {
		ip := net.ParseIP(addr)
		if !isBlockedIP(ip) {
			t.Errorf("expected %s to be blocked", addr)
		}
	}

	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"2607:f8b0:4004:800::200e", // Google public v6
	}
	for _, addr := range allowed {
		ip := net.ParseIP(addr)
		if isBlockedIP(ip) {
			t.Errorf("expected %s to be allowed", addr)
		}
	}
}

func TestWebFetch_EmptyURL(t *testing.T) {
	resp, err := executeWebFetch(context.Background(), webFetchParams{URL: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error response for empty URL")
	}
}

func TestWebFetch_InvalidScheme(t *testing.T) {
	resp, err := executeWebFetch(context.Background(), webFetchParams{URL: "ftp://example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error response for non-http scheme")
	}
}

func TestWebFetch_BlocksLoopback(t *testing.T) {
	resp, err := executeWebFetch(context.Background(), webFetchParams{
		URL: "http://127.0.0.1:1/test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error for loopback address")
	}
}

func TestWebFetch_BlocksPrivate(t *testing.T) {
	resp, err := executeWebFetch(context.Background(), webFetchParams{
		URL: "http://10.0.0.1:1/test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error for private address")
	}
}

func TestWebFetch_BlocksLinkLocal(t *testing.T) {
	resp, err := executeWebFetch(context.Background(), webFetchParams{
		URL: "http://169.254.1.1:1/test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error for link-local address")
	}
}
