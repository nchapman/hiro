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

	// Should be "xxxx-xxxx" format.
	parts := strings.Split(code, "-")
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts separated by dash, got %q", code)
	}
	if len(parts[0]) != 4 {
		t.Fatalf("first part should be 4 chars, got %d", len(parts[0]))
	}
	if len(parts[1]) != 4 {
		t.Fatalf("second part should be 4 chars, got %d", len(parts[1]))
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
