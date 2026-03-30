package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsSameOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"no origin header", "", "localhost:8080", true},
		{"same origin http", "http://localhost:8080", "localhost:8080", true},
		{"same origin https", "https://localhost:8080", "localhost:8080", true},
		{"different origin", "http://evil.com", "localhost:8080", false},
		{"different port", "http://localhost:9090", "localhost:8080", false},
		{"no host header", "http://localhost:8080", "", false},
		{"origin without scheme matches host", "localhost:8080", "localhost:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/setup", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			req.Host = tt.host

			if got := isSameOrigin(req); got != tt.want {
				t.Errorf("isSameOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLoopbackOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"localhost with port", "http://localhost:8080", "localhost:8080", true},
		{"127.0.0.1 with port", "http://127.0.0.1:8080", "127.0.0.1:8080", true},
		{"::1 with port", "http://[::1]:8080", "[::1]:8080", true},
		{"localhost no port", "http://localhost", "localhost", true},
		{"external domain same-origin", "http://evil.local:8080", "evil.local:8080", false},
		{"DNS rebinding attack", "http://attacker.com", "attacker.com", false},
		{"no origin (non-browser)", "", "localhost:8080", true},
		{"cross-origin", "http://evil.com", "localhost:8080", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/setup", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			req.Host = tt.host

			if got := isLoopbackOrigin(req); got != tt.want {
				t.Errorf("isLoopbackOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		want       string
	}{
		{"remote addr strips port", "", "", "192.168.1.1:12345", "192.168.1.1"},
		{"xff trusted from private peer", "10.0.0.1", "", "192.168.1.1:12345", "10.0.0.1"},
		{"xff chain from private peer", "10.0.0.1, 10.0.0.2", "", "192.168.1.1:12345", "10.0.0.1"},
		{"xri trusted from loopback", "", "10.0.0.5", "127.0.0.1:12345", "10.0.0.5"},
		{"xff priority over xri", "10.0.0.1", "10.0.0.5", "192.168.1.1:12345", "10.0.0.1"},
		{"xff ignored from public peer", "10.0.0.1", "", "8.8.8.8:12345", "8.8.8.8"},
		{"xri ignored from public peer", "", "10.0.0.5", "8.8.8.8:12345", "8.8.8.8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     http.Header{},
			}
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-Ip", tt.xri)
			}
			if got := clientIP(req); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
