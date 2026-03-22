package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
)

func TestRedactor_Redact(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		input   string
		want    string
	}{
		{
			name:    "replaces secret value with name",
			secrets: []string{"GITHUB_TOKEN=ghp_abc123xyz"},
			input:   "remote: Invalid credentials ghp_abc123xyz",
			want:    "remote: Invalid credentials [GITHUB_TOKEN]",
		},
		{
			name:    "replaces multiple secrets",
			secrets: []string{"DB_PASS=hunter2_longpass", "API_KEY=sk-secret-key-1234"},
			input:   "connecting with hunter2_longpass to api sk-secret-key-1234",
			want:    "connecting with [DB_PASS] to api [API_KEY]",
		},
		{
			name:    "replaces multiple occurrences of same secret",
			secrets: []string{"TOKEN=abcdef1234"},
			input:   "sent abcdef1234, got abcdef1234 back",
			want:    "sent [TOKEN], got [TOKEN] back",
		},
		{
			name:    "skips short values to avoid false positives",
			secrets: []string{"SHORT=root"},
			input:   "root is a common substring",
			want:    "root is a common substring",
		},
		{
			name:    "handles exactly min length",
			secrets: []string{"EIGHT=abcdefgh"},
			input:   "value is abcdefgh here",
			want:    "value is [EIGHT] here",
		},
		{
			name:    "no secrets configured",
			secrets: nil,
			input:   "nothing to redact",
			want:    "nothing to redact",
		},
		{
			name:    "empty input",
			secrets: []string{"TOKEN=secret12345"},
			input:   "",
			want:    "",
		},
		{
			name:    "malformed secret entry without equals",
			secrets: []string{"NOSEPARATOR"},
			input:   "NOSEPARATOR should not crash",
			want:    "NOSEPARATOR should not crash",
		},
		{
			name:    "secret value with equals sign",
			secrets: []string{"COMPLEX=base64data==end"},
			input:   "encoded: base64data==end",
			want:    "encoded: [COMPLEX]",
		},
		{
			name: "longer secret matched before shorter prefix",
			secrets: []string{
				"SHORT_TOKEN=ghp_abc123",
				"LONG_TOKEN=ghp_abc123XYZ789",
			},
			input: "token: ghp_abc123XYZ789",
			want:  "token: [LONG_TOKEN]",
		},
		{
			name: "both prefix and full value redacted correctly",
			secrets: []string{
				"SHORT_TOKEN=ghp_abc123",
				"LONG_TOKEN=ghp_abc123XYZ789",
			},
			input: "short: ghp_abc123 long: ghp_abc123XYZ789",
			want:  "short: [SHORT_TOKEN] long: [LONG_TOKEN]",
		},
		{
			name:    "matching is case sensitive",
			secrets: []string{"TOKEN=MySecret123"},
			input:   "value is mysecret123",
			want:    "value is mysecret123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRedactor(func() []string { return tt.secrets })
			got := r.Redact(tt.input)
			if got != tt.want {
				t.Errorf("Redact() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedactor_Nil(t *testing.T) {
	var r *Redactor
	got := r.Redact("should pass through unchanged")
	if got != "should pass through unchanged" {
		t.Errorf("nil Redactor should be no-op, got %q", got)
	}
}

func TestNewRedactor_NilFn(t *testing.T) {
	r := NewRedactor(nil)
	if r != nil {
		t.Error("NewRedactor(nil) should return nil")
	}
}

func TestRedactingTool(t *testing.T) {
	inner := fantasy.NewAgentTool("test_tool", "a test tool",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("token is ghp_supersecret123"), nil
		},
	)

	redactor := NewRedactor(func() []string {
		return []string{"GH_TOKEN=ghp_supersecret123"}
	})

	wrapped := wrapToolsWithRedactor([]fantasy.AgentTool{inner}, redactor)
	if len(wrapped) != 1 {
		t.Fatalf("expected 1 wrapped tool, got %d", len(wrapped))
	}

	if wrapped[0].Info().Name != "test_tool" {
		t.Errorf("tool name = %q, want %q", wrapped[0].Info().Name, "test_tool")
	}

	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{
		ID:    "1",
		Name:  "test_tool",
		Input: "{}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "token is [GH_TOKEN]"
	if resp.Content != want {
		t.Errorf("redacted content = %q, want %q", resp.Content, want)
	}
}

func TestRedactingTool_ErrorResponse(t *testing.T) {
	inner := fantasy.NewAgentTool("fail_tool", "a failing tool",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextErrorResponse("auth failed with ghp_supersecret123"), nil
		},
	)

	redactor := NewRedactor(func() []string {
		return []string{"GH_TOKEN=ghp_supersecret123"}
	})

	wrapped := wrapToolsWithRedactor([]fantasy.AgentTool{inner}, redactor)
	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{
		ID:    "1",
		Name:  "fail_tool",
		Input: "{}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected IsError to be true")
	}
	want := "auth failed with [GH_TOKEN]"
	if resp.Content != want {
		t.Errorf("redacted error = %q, want %q", resp.Content, want)
	}
}

func TestRedactingTool_Metadata(t *testing.T) {
	inner := fantasy.NewAgentTool("meta_tool", "tool with metadata",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			resp := fantasy.NewTextResponse("clean content")
			resp.Metadata = "leaked ghp_supersecret123 in metadata"
			return resp, nil
		},
	)

	redactor := NewRedactor(func() []string {
		return []string{"GH_TOKEN=ghp_supersecret123"}
	})

	wrapped := wrapToolsWithRedactor([]fantasy.AgentTool{inner}, redactor)
	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{
		ID:    "1",
		Name:  "meta_tool",
		Input: "{}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantMeta := "leaked [GH_TOKEN] in metadata"
	if resp.Metadata != wantMeta {
		t.Errorf("metadata = %q, want %q", resp.Metadata, wantMeta)
	}
}

func TestRedactingTool_NilRedactor(t *testing.T) {
	inner := fantasy.NewAgentTool("passthrough", "no redaction",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("secret_value"), nil
		},
	)

	wrapped := wrapToolsWithRedactor([]fantasy.AgentTool{inner}, nil)
	if wrapped[0] != inner {
		t.Error("nil redactor should return original tools")
	}
}

func TestRedactor_DynamicSecrets(t *testing.T) {
	secrets := []string{"TOKEN=first_value_long"}
	r := NewRedactor(func() []string { return secrets })

	got := r.Redact("using first_value_long here")
	if got != "using [TOKEN] here" {
		t.Errorf("first call = %q, want %q", got, "using [TOKEN] here")
	}

	// Update secrets (simulates /secrets set at runtime).
	secrets = []string{"TOKEN=second_value_long"}
	got = r.Redact("now using second_value_long here")
	if got != "now using [TOKEN] here" {
		t.Errorf("after update = %q, want %q", got, "now using [TOKEN] here")
	}
}
