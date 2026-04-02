package models

import "strings"

// ModelSpec is a parsed provider/model reference. This is the canonical
// representation of "which model on which provider" throughout the codebase.
//
// User-facing config uses the string form "provider/model":
//
//	anthropic/claude-sonnet-4-20250514
//	openrouter/anthropic/claude-sonnet-4-20250514
//	claude-sonnet-4-20250514  (bare model — provider resolved from default)
//
// The first "/" splits provider from model. OpenRouter model names naturally
// contain slashes (e.g. "anthropic/claude-sonnet-4-20250514"), so the split
// is only on the first "/".
type ModelSpec struct {
	Provider string // e.g. "anthropic", "openrouter"; empty = resolve from default
	Model    string // e.g. "claude-sonnet-4-20250514", "anthropic/claude-sonnet-4-20250514"
}

// ParseModelSpec parses a "provider/model" string into its components.
//
//	"anthropic/claude-sonnet-4-20250514"           → Provider="anthropic", Model="claude-sonnet-4-20250514"
//	"openrouter/anthropic/claude-sonnet-4-20250514" → Provider="openrouter", Model="anthropic/claude-sonnet-4-20250514"
//	"claude-sonnet-4-20250514"                      → Provider="", Model="claude-sonnet-4-20250514"
//	""                                              → Provider="", Model=""
func ParseModelSpec(s string) ModelSpec {
	s = strings.TrimSpace(s)
	if s == "" {
		return ModelSpec{}
	}
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return ModelSpec{Model: s}
	}
	return ModelSpec{
		Provider: s[:i],
		Model:    s[i+1:],
	}
}

// String returns the canonical "provider/model" form, or just the model
// if no provider is specified.
func (ms ModelSpec) String() string {
	if ms.Provider == "" {
		return ms.Model
	}
	return ms.Provider + "/" + ms.Model
}

// IsEmpty reports whether neither provider nor model is set.
func (ms ModelSpec) IsEmpty() bool {
	return ms.Provider == "" && ms.Model == ""
}
