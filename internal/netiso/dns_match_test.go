package netiso

import "testing"

func TestMatchDomain(t *testing.T) {
	tests := []struct {
		query     string
		allowlist []string
		want      bool
	}{
		// Exact match.
		{"github.com", []string{"github.com"}, true},
		{"GITHUB.COM", []string{"github.com"}, true}, // case insensitive
		{"github.com", []string{"gitlab.com"}, false},

		// Wildcard *.
		{"api.github.com", []string{"*.github.com"}, true},
		{"foo.bar.github.com", []string{"*.github.com"}, true},
		{"github.com", []string{"*.github.com"}, false}, // wildcard does NOT match base
		{"notgithub.com", []string{"*.github.com"}, false},
		{"xgithub.com", []string{"*.github.com"}, false},

		// Global wildcard.
		{"anything.com", []string{"*"}, true},
		{"evil.org", []string{"*"}, true},

		// Multiple entries.
		{"github.com", []string{"pypi.org", "github.com"}, true},
		{"api.github.com", []string{"github.com", "*.github.com"}, true},
		{"evil.com", []string{"github.com", "*.github.com"}, false},

		// Empty allowlist.
		{"github.com", nil, false},
		{"github.com", []string{}, false},
	}

	for _, tt := range tests {
		got := MatchDomain(tt.query, tt.allowlist)
		if got != tt.want {
			t.Errorf("MatchDomain(%q, %v) = %v, want %v", tt.query, tt.allowlist, got, tt.want)
		}
	}
}
