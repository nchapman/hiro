package cluster

import (
	"strings"
	"testing"
)

func TestGenerateSwarmCode_Format(t *testing.T) {
	t.Parallel()

	code, err := GenerateSwarmCode()
	if err != nil {
		t.Fatalf("GenerateSwarmCode: %v", err)
	}

	// Should be "xxxx-xxxx-xxxx" format.
	parts := strings.Split(code, "-")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts separated by dashes, got %q", code)
	}
	for i, p := range parts {
		if len(p) != 4 {
			t.Fatalf("part %d should be 4 chars, got %d in %q", i, len(p), code)
		}
	}
}

func TestGenerateSwarmCode_ValidCharset(t *testing.T) {
	t.Parallel()

	const charset = "abcdefghjkmnpqrstuvwxyz23456789"
	valid := map[byte]bool{}
	for i := range len(charset) {
		valid[charset[i]] = true
	}
	valid['-'] = true

	for range 20 {
		code, err := GenerateSwarmCode()
		if err != nil {
			t.Fatalf("GenerateSwarmCode: %v", err)
		}
		for i := range len(code) {
			if !valid[code[i]] {
				t.Fatalf("invalid character %q in code %q", code[i], code)
			}
		}
	}
}

func TestGenerateSwarmCode_NoAmbiguousChars(t *testing.T) {
	t.Parallel()

	// Ambiguous characters that should be excluded: 0, o, 1, l, i
	ambiguous := "0o1li"
	for range 50 {
		code, err := GenerateSwarmCode()
		if err != nil {
			t.Fatalf("GenerateSwarmCode: %v", err)
		}
		for _, c := range ambiguous {
			if strings.ContainsRune(code, c) {
				t.Fatalf("code %q contains ambiguous character %q", code, string(c))
			}
		}
	}
}

func TestGenerateSwarmCode_Unique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for range 100 {
		code, err := GenerateSwarmCode()
		if err != nil {
			t.Fatalf("GenerateSwarmCode: %v", err)
		}
		if seen[code] {
			t.Fatalf("duplicate code generated: %q", code)
		}
		seen[code] = true
	}
}
