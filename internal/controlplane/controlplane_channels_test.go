package controlplane

import (
	"testing"
)

func TestResolveSecret_LiteralValue(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	// Not a reference — return as-is.
	if v := cp.ResolveSecret("plain-token"); v != "plain-token" {
		t.Errorf("got %q, want %q", v, "plain-token")
	}
}

func TestResolveSecret_Reference(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()
	cp.config.Secrets["MY_TOKEN"] = "secret-value"

	if v := cp.ResolveSecret("${MY_TOKEN}"); v != "secret-value" {
		t.Errorf("got %q, want %q", v, "secret-value")
	}
}

func TestResolveSecret_MissingSecret(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	if v := cp.ResolveSecret("${NONEXISTENT}"); v != "" {
		t.Errorf("got %q, want empty", v)
	}
}

func TestResolveSecret_EmptyString(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	if v := cp.ResolveSecret(""); v != "" {
		t.Errorf("got %q, want empty", v)
	}
}

func TestResolveSecret_PartialSyntax(t *testing.T) {
	t.Parallel()

	cp := &ControlPlane{config: Config{}}
	cp.config.initMaps()

	// Missing closing brace — not a reference.
	if v := cp.ResolveSecret("${UNCLOSED"); v != "${UNCLOSED" {
		t.Errorf("got %q", v)
	}

	// Missing opening — not a reference.
	if v := cp.ResolveSecret("NOCURLY}"); v != "NOCURLY}" {
		t.Errorf("got %q", v)
	}
}
