package models

import "testing"

func TestParseModelSpec(t *testing.T) {
	tests := []struct {
		input    string
		provider string
		model    string
	}{
		{"", "", ""},
		{"  ", "", ""},
		{"claude-sonnet-4-20250514", "", "claude-sonnet-4-20250514"},
		{"anthropic/claude-sonnet-4-20250514", "anthropic", "claude-sonnet-4-20250514"},
		{"openrouter/anthropic/claude-sonnet-4-20250514", "openrouter", "anthropic/claude-sonnet-4-20250514"},
		{"openai/gpt-4o", "openai", "gpt-4o"},
	}

	for _, tt := range tests {
		spec := ParseModelSpec(tt.input)
		if spec.Provider != tt.provider || spec.Model != tt.model {
			t.Errorf("ParseModelSpec(%q) = {%q, %q}, want {%q, %q}",
				tt.input, spec.Provider, spec.Model, tt.provider, tt.model)
		}
	}
}

func TestModelSpec_String(t *testing.T) {
	tests := []struct {
		spec ModelSpec
		want string
	}{
		{ModelSpec{}, ""},
		{ModelSpec{Model: "claude-sonnet-4-20250514"}, "claude-sonnet-4-20250514"},
		{ModelSpec{Provider: "anthropic", Model: "claude-sonnet-4-20250514"}, "anthropic/claude-sonnet-4-20250514"},
		{ModelSpec{Provider: "openrouter", Model: "anthropic/claude-sonnet-4-20250514"}, "openrouter/anthropic/claude-sonnet-4-20250514"},
	}

	for _, tt := range tests {
		got := tt.spec.String()
		if got != tt.want {
			t.Errorf("ModelSpec{%q, %q}.String() = %q, want %q",
				tt.spec.Provider, tt.spec.Model, got, tt.want)
		}
	}
}

func TestModelSpec_IsEmpty(t *testing.T) {
	if !(ModelSpec{}).IsEmpty() {
		t.Error("zero ModelSpec should be empty")
	}
	if (ModelSpec{Model: "x"}).IsEmpty() {
		t.Error("ModelSpec with model should not be empty")
	}
	if (ModelSpec{Provider: "x"}).IsEmpty() {
		t.Error("ModelSpec with provider should not be empty")
	}
}

func TestParseModelSpec_Roundtrip(t *testing.T) {
	inputs := []string{
		"anthropic/claude-sonnet-4-20250514",
		"openrouter/anthropic/claude-sonnet-4-20250514",
		"claude-sonnet-4-20250514",
	}
	for _, input := range inputs {
		spec := ParseModelSpec(input)
		if got := spec.String(); got != input {
			t.Errorf("roundtrip failed: %q → %q", input, got)
		}
	}
}
