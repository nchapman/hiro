package api

import (
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
