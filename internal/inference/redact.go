package inference

import (
	"cmp"
	"slices"
	"strings"
)

// minSecretLen is the minimum length a secret value must have to be redacted.
const minSecretLen = 8

// Redactor replaces secret values with their names in text output.
type Redactor struct {
	secretsFn func() []string // returns KEY=VALUE pairs
}

// NewRedactor creates a redactor that pulls secrets from the given function.
// If secretsFn is nil, returns nil (no-op redactor).
func NewRedactor(secretsFn func() []string) *Redactor {
	if secretsFn == nil {
		return nil
	}
	return &Redactor{secretsFn: secretsFn}
}

// Redact replaces all secret values in text with [SECRET_NAME].
func (r *Redactor) Redact(text string) string {
	if r == nil {
		return text
	}
	secrets := r.secretsFn()
	if len(secrets) == 0 {
		return text
	}

	type pair struct {
		name, value string
	}
	pairs := make([]pair, 0, len(secrets))
	for _, kv := range secrets {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || len(value) < minSecretLen {
			continue
		}
		pairs = append(pairs, pair{name, value})
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		return cmp.Compare(len(b.value), len(a.value))
	})

	for _, p := range pairs {
		text = strings.ReplaceAll(text, p.value, "["+p.name+"]")
	}
	return text
}
