package agent

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"charm.land/fantasy"
)

// minSecretLen is the minimum length a secret value must have to be redacted.
// Short values like "root" or "true" cause false positives in tool output.
// Operators should use secrets of reasonable length; short values are skipped.
const minSecretLen = 8

// Redactor replaces secret values with their names in text output.
// It fetches current secrets lazily on each call so runtime changes
// (via /secrets set) take effect immediately.
type Redactor struct {
	secretsFn func() []string // returns KEY=VALUE pairs
}

// NewRedactor creates a redactor that pulls secrets from the given function.
// If secretsFn is nil, the redactor is a no-op.
func NewRedactor(secretsFn func() []string) *Redactor {
	if secretsFn == nil {
		return nil
	}
	return &Redactor{secretsFn: secretsFn}
}

// secretPair holds a parsed secret name and value for redaction.
type secretPair struct {
	name  string
	value string
}

// Redact replaces all secret values in text with [SECRET_NAME].
// Secrets are replaced longest-first to prevent a shorter secret that
// is a prefix of a longer one from corrupting the longer match.
func (r *Redactor) Redact(text string) string {
	if r == nil {
		return text
	}
	secrets := r.secretsFn()
	if len(secrets) == 0 {
		return text
	}

	// Parse and filter secrets, then sort longest-first.
	pairs := make([]secretPair, 0, len(secrets))
	for _, kv := range secrets {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || len(value) < minSecretLen {
			continue
		}
		pairs = append(pairs, secretPair{name, value})
	}
	slices.SortFunc(pairs, func(a, b secretPair) int {
		return cmp.Compare(len(b.value), len(a.value)) // longest first
	})

	for _, p := range pairs {
		text = strings.ReplaceAll(text, p.value, "["+p.name+"]")
	}
	return text
}

// redactingTool wraps a fantasy.AgentTool and redacts secret values
// from the tool's text output before it reaches the LLM.
type redactingTool struct {
	inner    fantasy.AgentTool
	redactor *Redactor
}

func (t *redactingTool) Info() fantasy.ToolInfo {
	return t.inner.Info()
}

func (t *redactingTool) Run(ctx context.Context, params fantasy.ToolCall) (fantasy.ToolResponse, error) {
	resp, err := t.inner.Run(ctx, params)
	if err != nil {
		return resp, err
	}
	resp.Content = t.redactor.Redact(resp.Content)
	resp.Metadata = t.redactor.Redact(resp.Metadata)
	return resp, nil
}

func (t *redactingTool) ProviderOptions() fantasy.ProviderOptions {
	return t.inner.ProviderOptions()
}

func (t *redactingTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.inner.SetProviderOptions(opts)
}

// wrapToolsWithRedactor wraps each tool so its output is redacted.
// If redactor is nil, tools are returned unchanged.
func wrapToolsWithRedactor(tools []fantasy.AgentTool, redactor *Redactor) []fantasy.AgentTool {
	if redactor == nil {
		return tools
	}
	wrapped := make([]fantasy.AgentTool, len(tools))
	for i, t := range tools {
		wrapped[i] = &redactingTool{inner: t, redactor: redactor}
	}
	return wrapped
}
