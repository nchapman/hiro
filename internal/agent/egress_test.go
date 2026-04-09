package agent

import (
	"reflect"
	"testing"
)

func TestIntersectEgress(t *testing.T) {
	tests := []struct {
		name   string
		child  []string
		parent []string
		want   []string
	}{
		{
			name:   "parent wildcard, child specific",
			child:  []string{"github.com"},
			parent: []string{"*"},
			want:   []string{"github.com"},
		},
		{
			name:   "child wildcard, parent specific",
			child:  []string{"*"},
			parent: []string{"github.com"},
			want:   []string{"github.com"},
		},
		{
			name:   "both wildcard",
			child:  []string{"*"},
			parent: []string{"*"},
			want:   []string{"*"},
		},
		{
			name:   "exact overlap",
			child:  []string{"github.com"},
			parent: []string{"github.com", "pypi.org"},
			want:   []string{"github.com"},
		},
		{
			name:   "no overlap",
			child:  []string{"github.com"},
			parent: []string{"npmjs.org"},
			want:   nil,
		},
		{
			name:   "parent wildcard covers child subdomain",
			child:  []string{"api.github.com"},
			parent: []string{"*.github.com"},
			want:   []string{"api.github.com"},
		},
		{
			name:   "child wildcard under parent wildcard",
			child:  []string{"*.github.com"},
			parent: []string{"*.github.com"},
			want:   []string{"*.github.com"},
		},
		{
			name:   "parent wildcard does not cover unrelated",
			child:  []string{"evil.com"},
			parent: []string{"*.github.com"},
			want:   nil,
		},
		{
			name:   "multiple entries, partial overlap",
			child:  []string{"github.com", "pypi.org", "evil.com"},
			parent: []string{"github.com", "npmjs.org", "pypi.org"},
			want:   []string{"github.com", "pypi.org"},
		},
		{
			name:   "empty child",
			child:  []string{},
			parent: []string{"github.com"},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intersectEgress(tt.child, tt.parent)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("intersectEgress(%v, %v) = %v, want %v", tt.child, tt.parent, got, tt.want)
			}
		})
	}
}

func TestEgressCovers(t *testing.T) {
	tests := []struct {
		list   []string
		domain string
		want   bool
	}{
		{[]string{"github.com"}, "github.com", true},
		{[]string{"github.com"}, "api.github.com", false},
		{[]string{"*.github.com"}, "api.github.com", true},
		{[]string{"*.github.com"}, "deep.api.github.com", true},
		{[]string{"*.github.com"}, "github.com", false},
		{[]string{"*.github.com"}, "notgithub.com", false},
		{[]string{"*.github.com"}, "*.api.github.com", true}, // child wildcard under parent wildcard
	}

	for _, tt := range tests {
		got := egressCovers(tt.list, tt.domain)
		if got != tt.want {
			t.Errorf("egressCovers(%v, %q) = %v, want %v", tt.list, tt.domain, got, tt.want)
		}
	}
}
