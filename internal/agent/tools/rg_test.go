package tools

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestIsRgUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"errRgUnavailable", errRgUnavailable, true},
		{"exec.ErrNotFound", exec.ErrNotFound, true},
		{"wrapped errRgUnavailable", errors.Join(errors.New("outer"), errRgUnavailable), true},
		{"other error", errors.New("something else"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRgUnavailable(tt.err)
			if got != tt.want {
				t.Errorf("isRgUnavailable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRgGlobCmd_WithPattern(t *testing.T) {
	// If rg is not available, the function returns nil — both cases are valid.
	cmd := rgGlobCmd(context.Background(), "*.go")
	if cmd == nil {
		t.Skip("ripgrep not available")
	}

	// Verify the command has expected arguments.
	found := false
	for _, arg := range cmd.Args {
		if arg == "*.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected *.go in args, got %v", cmd.Args)
	}
}

func TestRgGlobCmd_EmptyPattern(t *testing.T) {
	cmd := rgGlobCmd(context.Background(), "")
	if cmd == nil {
		t.Skip("ripgrep not available")
	}

	// With empty pattern, should not include a glob for the pattern itself.
	for i, arg := range cmd.Args {
		if arg == "--glob" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "" {
			t.Error("should not have an empty --glob argument for the pattern")
		}
	}
}

func TestRgExcludeGlobs(t *testing.T) {
	if len(rgExcludeGlobs) == 0 {
		t.Fatal("rgExcludeGlobs should not be empty")
	}
	for _, g := range rgExcludeGlobs {
		if g[0] != '!' {
			t.Errorf("exclude glob %q should start with '!'", g)
		}
	}
}
