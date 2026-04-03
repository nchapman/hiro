package cluster

import (
	"os"
	"testing"
)

func TestWorkerTerminalEnv(t *testing.T) {
	t.Parallel()

	env := workerTerminalEnv()

	// Should always have the fixed entries.
	has := map[string]bool{}
	for _, e := range env {
		has[e] = true
	}
	if !has["TERM=xterm-256color"] {
		t.Error("missing TERM=xterm-256color")
	}
	if !has["LANG=en_US.UTF-8"] {
		t.Error("missing LANG=en_US.UTF-8")
	}
	if !has["LC_ALL=en_US.UTF-8"] {
		t.Error("missing LC_ALL=en_US.UTF-8")
	}

	// Should include PATH from environment if set.
	if pathVal := os.Getenv("PATH"); pathVal != "" {
		found := false
		for _, e := range env {
			if e == "PATH="+pathVal {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected PATH from environment to be included")
		}
	}
}

func TestWorkerTerminalEnv_IncludesSetVars(t *testing.T) {
	// Set a var that workerTerminalEnv checks for and verify it appears.
	t.Setenv("STARSHIP_CONFIG", "/tmp/starship.toml")

	env := workerTerminalEnv()
	found := false
	for _, e := range env {
		if e == "STARSHIP_CONFIG=/tmp/starship.toml" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected STARSHIP_CONFIG to be included in terminal env")
	}
}
