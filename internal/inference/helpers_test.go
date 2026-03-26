package inference

import (
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hi", 1},      // len=2, 2/4=0 but >0 → 1
		{"abc", 1},      // len=3, 3/4=0 but >0 → 1
		{"abcd", 1},     // len=4, 4/4=1
		{"hello world!", 3}, // len=12, 12/4=3
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestResolveStatusMessage(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{
			name: "simple template",
			tool: "read_file", input: `{"path":"main.go"}`,
			want: "Reading main.go",
		},
		{
			name: "no template params",
			tool: "bash", input: `{"command":"ls"}`,
			want: "Running command",
		},
		{
			name: "unknown tool",
			tool: "unknown_tool", input: `{}`,
			want: "",
		},
		{
			name: "missing required param",
			tool: "read_file", input: `{}`,
			want: "", // unresolved placeholder → empty
		},
		{
			name: "empty input JSON",
			tool: "read_file", input: "",
			want: "",
		},
		{
			name: "glob with pattern",
			tool: "glob", input: `{"pattern":"**/*.go"}`,
			want: "Searching for **/*.go",
		},
		{
			name: "non-string param value",
			tool: "read_file", input: `{"path": 42}`,
			want: "", // placeholder not substituted → empty
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveStatusMessage(tt.tool, tt.input)
			if got != tt.want {
				t.Errorf("resolveStatusMessage(%q, %q) = %q, want %q", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateResult(t *testing.T) {
	short := "hello"
	if got := truncateResult(short); got != short {
		t.Errorf("short string should not be truncated")
	}

	long := make([]byte, maxAgentResultSize+100)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateResult(string(long))
	if len(got) > maxAgentResultSize+50 {
		t.Errorf("truncated result too long: %d", len(got))
	}
	if got[len(got)-len("(result truncated)"):] != "(result truncated)" {
		t.Error("expected truncation marker")
	}
}

func TestInjectStatusMessages_NoToolCalls(t *testing.T) {
	raw := `{"role":"user","content":[{"type":"text","data":{"text":"hello"}}]}`
	got := InjectStatusMessages(raw)
	if got != raw {
		t.Error("should not modify messages without tool calls")
	}
}

func TestInjectStatusMessages_InvalidJSON(t *testing.T) {
	raw := "not json"
	got := InjectStatusMessages(raw)
	if got != raw {
		t.Error("should return original on invalid JSON")
	}
}
