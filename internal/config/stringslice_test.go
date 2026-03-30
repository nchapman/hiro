package config

import "testing"

func TestStringSlice(t *testing.T) {
	tests := []struct {
		name string
		fm   Frontmatter
		key  string
		want []string
	}{
		{
			name: "normal string slice",
			fm:   Frontmatter{"tools": []any{"bash", "read_file", "grep"}},
			key:  "tools",
			want: []string{"bash", "read_file", "grep"},
		},
		{
			name: "missing key",
			fm:   Frontmatter{},
			key:  "tools",
			want: nil,
		},
		{
			name: "wrong type (string instead of slice)",
			fm:   Frontmatter{"tools": "bash"},
			key:  "tools",
			want: nil,
		},
		{
			name: "wrong type (int instead of slice)",
			fm:   Frontmatter{"tools": 42},
			key:  "tools",
			want: nil,
		},
		{
			name: "empty slice",
			fm:   Frontmatter{"tools": []any{}},
			key:  "tools",
			want: []string{},
		},
		{
			name: "mixed types in slice (non-strings skipped)",
			fm:   Frontmatter{"items": []any{"hello", 42, true, "world"}},
			key:  "items",
			want: []string{"hello", "world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fm.StringSlice(tt.key)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len=%d, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
