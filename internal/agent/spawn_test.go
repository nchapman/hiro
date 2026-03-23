package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/nchapman/hivebot/internal/ipc"
)

func TestBuildIsolatedEnv_CoreVars(t *testing.T) {
	cfg := ipc.SpawnConfig{
		SessionDir: "/hive/sessions/abc-123",
		APIKey:     "sk-test-key",
	}
	getenv := func(key string) string {
		if key == "PATH" {
			return "/opt/mise/shims:/usr/local/bin:/usr/bin"
		}
		return ""
	}

	env := buildIsolatedEnv(cfg, getenv)

	expect := map[string]string{
		"PATH":         "/opt/mise/shims:/usr/local/bin:/usr/bin",
		"HOME":         "/hive/sessions/abc-123",
		"LANG":         "en_US.UTF-8",
		"LC_ALL":       "en_US.UTF-8",
		"HIVE_API_KEY": "sk-test-key",
	}
	envMap := parseEnv(env)

	for k, want := range expect {
		got, ok := envMap[k]
		if !ok {
			t.Errorf("missing %s", k)
		} else if got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestBuildIsolatedEnv_HomeIsSessionDir(t *testing.T) {
	cfg := ipc.SpawnConfig{SessionDir: "/hive/sessions/agent-xyz"}
	env := buildIsolatedEnv(cfg, func(string) string { return "" })
	envMap := parseEnv(env)

	if envMap["HOME"] != "/hive/sessions/agent-xyz" {
		t.Errorf("HOME = %q, want session dir", envMap["HOME"])
	}
}

func TestBuildIsolatedEnv_ForwardsMiseVars(t *testing.T) {
	cfg := ipc.SpawnConfig{SessionDir: "/sessions/test"}
	getenv := func(key string) string {
		switch key {
		case "MISE_DATA_DIR":
			return "/opt/mise"
		case "MISE_CONFIG_DIR":
			return "/opt/mise/config"
		case "MISE_CACHE_DIR":
			return "/opt/mise/cache"
		case "MISE_GLOBAL_CONFIG_FILE":
			return "/opt/mise/config/config.toml"
		}
		return ""
	}

	env := buildIsolatedEnv(cfg, getenv)
	envMap := parseEnv(env)

	for _, tc := range []struct{ key, want string }{
		{"MISE_DATA_DIR", "/opt/mise"},
		{"MISE_CONFIG_DIR", "/opt/mise/config"},
		{"MISE_CACHE_DIR", "/opt/mise/cache"},
		{"MISE_GLOBAL_CONFIG_FILE", "/opt/mise/config/config.toml"},
	} {
		if envMap[tc.key] != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, envMap[tc.key], tc.want)
		}
	}
}

func TestBuildIsolatedEnv_OmitsMiseVarsWhenUnset(t *testing.T) {
	cfg := ipc.SpawnConfig{SessionDir: "/sessions/test"}
	env := buildIsolatedEnv(cfg, func(string) string { return "" })

	for _, entry := range env {
		if strings.HasPrefix(entry, "MISE_") {
			t.Errorf("unexpected mise var in env: %s", entry)
		}
	}
}

func TestBuildIsolatedEnv_NoExtraVars(t *testing.T) {
	cfg := ipc.SpawnConfig{SessionDir: "/sessions/test"}
	getenv := func(key string) string {
		switch key {
		case "MISE_DATA_DIR":
			return "/opt/mise"
		case "MISE_CONFIG_DIR":
			return "/opt/mise/config"
		case "MISE_CACHE_DIR":
			return "/opt/mise/cache"
		case "MISE_GLOBAL_CONFIG_FILE":
			return "/opt/mise/config/config.toml"
		}
		return ""
	}

	env := buildIsolatedEnv(cfg, getenv)

	// Should contain exactly these vars — nothing else.
	allowed := []string{"PATH", "HOME", "LANG", "LC_ALL", "HIVE_API_KEY",
		"MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_GLOBAL_CONFIG_FILE"}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(allowed, key) {
			t.Errorf("unexpected env var: %s", key)
		}
	}
	if len(env) != len(allowed) {
		t.Errorf("env has %d entries, want %d", len(env), len(allowed))
	}
}

func TestBuildIsolatedEnv_DoesNotLeakControlPlaneVars(t *testing.T) {
	cfg := ipc.SpawnConfig{SessionDir: "/sessions/test"}
	// Simulate control plane env with vars that should NOT be forwarded.
	getenv := func(key string) string {
		switch key {
		case "PATH":
			return "/usr/bin"
		case "SECRET_INTERNAL_TOKEN":
			return "should-not-appear"
		case "DATABASE_URL":
			return "should-not-appear"
		case "AWS_SECRET_ACCESS_KEY":
			return "should-not-appear"
		}
		return ""
	}

	env := buildIsolatedEnv(cfg, getenv)
	envMap := parseEnv(env)

	for _, dangerous := range []string{"SECRET_INTERNAL_TOKEN", "DATABASE_URL", "AWS_SECRET_ACCESS_KEY"} {
		if _, ok := envMap[dangerous]; ok {
			t.Errorf("%s leaked into agent environment", dangerous)
		}
	}
}

func TestBuildIsolatedEnv_PathIncludesMiseShims(t *testing.T) {
	cfg := ipc.SpawnConfig{SessionDir: "/sessions/test"}
	getenv := func(key string) string {
		if key == "PATH" {
			return "/opt/mise/shims:/usr/local/bin:/usr/bin"
		}
		return ""
	}

	env := buildIsolatedEnv(cfg, getenv)
	envMap := parseEnv(env)

	path := envMap["PATH"]
	if !strings.Contains(path, "/opt/mise/shims") {
		t.Errorf("PATH %q does not contain mise shims", path)
	}
}

func TestForwardedEnvKeys_ContainsMiseVars(t *testing.T) {
	// Guard against accidentally removing mise vars from the forwarded list.
	required := []string{"MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_GLOBAL_CONFIG_FILE"}
	for _, key := range required {
		if !slices.Contains(forwardedEnvKeys, key) {
			t.Errorf("forwardedEnvKeys missing %q", key)
		}
	}
}

// parseEnv converts a []string of "KEY=VALUE" entries into a map.
func parseEnv(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		m[key] = value
	}
	return m
}
