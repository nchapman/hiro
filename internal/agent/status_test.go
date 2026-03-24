package agent

import (
	"encoding/json"
	"testing"
)

func TestResolveStatusMessage(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		inputJSON string
		want      string
	}{
		{
			name:      "known tool with param",
			toolName:  "read_file",
			inputJSON: `{"path": "main.go", "offset": 10}`,
			want:      "Reading main.go",
		},
		{
			name:      "known tool with multiple params",
			toolName:  "grep",
			inputJSON: `{"pattern": "TODO", "path": "src/"}`,
			want:      "Searching for TODO",
		},
		{
			name:      "known tool no params in template",
			toolName:  "bash",
			inputJSON: `{"command": "ls -la"}`,
			want:      "Running command",
		},
		{
			name:      "unknown tool",
			toolName:  "nonexistent",
			inputJSON: `{"foo": "bar"}`,
			want:      "",
		},
		{
			name:      "empty input JSON falls back to empty",
			toolName:  "read_file",
			inputJSON: "",
			want:      "",
		},
		{
			name:      "invalid JSON falls back to empty",
			toolName:  "read_file",
			inputJSON: "not json",
			want:      "",
		},
		{
			name:      "missing param falls back to empty",
			toolName:  "read_file",
			inputJSON: `{"offset": 5}`,
			want:      "",
		},
		{
			name:      "non-string param falls back to empty",
			toolName:  "read_file",
			inputJSON: `{"path": 123}`,
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveStatusMessage(tt.toolName, tt.inputJSON)
			if got != tt.want {
				t.Errorf("resolveStatusMessage(%q, %q) = %q, want %q",
					tt.toolName, tt.inputJSON, got, tt.want)
			}
		})
	}
}

func TestInjectStatusMessages(t *testing.T) {
	t.Run("injects status into tool-call entries", func(t *testing.T) {
		raw := `{"role":"assistant","content":[{"type":"text","data":{"text":"hello"}},{"type":"tool-call","data":{"tool_call_id":"c1","tool_name":"read_file","input":"{\"path\":\"main.go\"}"}}]}`
		got := injectStatusMessages(raw)

		var msg struct {
			Content []struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			} `json:"content"`
		}
		if err := json.Unmarshal([]byte(got), &msg); err != nil {
			t.Fatalf("unmarshal patched JSON: %v", err)
		}

		if len(msg.Content) != 2 {
			t.Fatalf("expected 2 content parts, got %d", len(msg.Content))
		}

		// Text part should have no status.
		if _, ok := msg.Content[0].Data["status"]; ok {
			t.Error("text part should not have status")
		}

		// Tool-call part should have resolved status.
		status, ok := msg.Content[1].Data["status"].(string)
		if !ok {
			t.Fatal("tool-call part missing status")
		}
		if status != "Reading main.go" {
			t.Errorf("status = %q, want %q", status, "Reading main.go")
		}
	})

	t.Run("returns original for non-assistant messages", func(t *testing.T) {
		raw := `{"role":"user","content":[{"type":"text","data":{"text":"hi"}}]}`
		got := injectStatusMessages(raw)
		if got != raw {
			t.Errorf("expected unchanged JSON for user message")
		}
	})

	t.Run("returns original for invalid JSON", func(t *testing.T) {
		raw := `not json`
		got := injectStatusMessages(raw)
		if got != raw {
			t.Errorf("expected unchanged string for invalid JSON")
		}
	})

	t.Run("returns original when no tool calls", func(t *testing.T) {
		raw := `{"role":"assistant","content":[{"type":"text","data":{"text":"hello"}}]}`
		got := injectStatusMessages(raw)
		if got != raw {
			t.Errorf("expected unchanged JSON when no tool calls")
		}
	})

	t.Run("unknown tool gets no status", func(t *testing.T) {
		raw := `{"role":"assistant","content":[{"type":"tool-call","data":{"tool_call_id":"c1","tool_name":"unknown_tool","input":"{}"}}]}`
		got := injectStatusMessages(raw)
		if got != raw {
			t.Errorf("expected unchanged JSON for unknown tool")
		}
	})
}
