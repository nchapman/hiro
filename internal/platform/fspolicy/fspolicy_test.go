package fspolicy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestExpand(t *testing.T) {
	vars := map[string]string{"HOME": "/home/hiro", "INSTANCE_DIR": "/home/hiro/instances/abc"}
	env := func(k string) string {
		if k == "MISE_DATA_DIR" {
			return "/opt/mise-data"
		}
		return ""
	}

	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"$HOME/workspace", "/home/hiro/workspace", true},
		{"$INSTANCE_DIR", "/home/hiro/instances/abc", true},
		{"$MISE_DATA_DIR", "/opt/mise-data", true},
		{"$UNSET_VAR", "", false}, // env returns empty
		{"/literal/path", "/literal/path", true},
		{"relative/path", "", false},       // must be absolute
		{"$HOME/../etc/passwd", "", false}, // traversal rejected
	}

	for _, c := range cases {
		got, ok := expand(c.in, vars, env)
		if ok != c.ok {
			t.Errorf("expand(%q): ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("expand(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

func TestExpandSinglePass_NoRecursion(t *testing.T) {
	// Expanded values must NOT be re-scanned — otherwise a self-referential
	// env var like MISE_DATA_DIR=$MISE_DATA_DIR/x would loop forever. We
	// expect the $MISE_DATA_DIR literal in the value to remain as-is (and
	// the overall path to be treated as absolute because it starts with /).
	vars := map[string]string{"HOME": "/home/hiro"}
	env := func(k string) string {
		if k == "SELF_REF" {
			return "/abs/$SELF_REF"
		}
		return ""
	}
	got, ok := expand("$SELF_REF/leaf", vars, env)
	if !ok {
		t.Fatalf("expected expansion to succeed")
	}
	if got != "/abs/$SELF_REF/leaf" {
		t.Errorf("expected single-pass expansion, got %q", got)
	}
}

func TestParseDefault(t *testing.T) {
	p, err := Parse(Default())
	if err != nil {
		t.Fatalf("parse default: %v", err)
	}
	if p.Version != 1 {
		t.Errorf("version = %d, want 1", p.Version)
	}
	if len(p.Base.RW) == 0 || len(p.Base.RO) == 0 {
		t.Errorf("base rules empty")
	}
	if _, ok := p.OnTool["Bash"]; !ok {
		t.Errorf("missing Bash on_tool rules")
	}
	if _, ok := p.OnTool["CreatePersistentInstance"]; !ok {
		t.Errorf("missing CreatePersistentInstance on_tool rules")
	}
}

func TestParseRejectsUnsupportedVersion(t *testing.T) {
	_, err := Parse([]byte("version: 2\n"))
	if err == nil {
		t.Errorf("expected error for version 2")
	}
}

func TestCompilePromotesAgentsToRW(t *testing.T) {
	// With CreatePersistentInstance, $HOME/agents should be promoted from RO to RW.
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "agents"))
	mustMkdir(t, filepath.Join(home, "skills"))
	mustMkdir(t, filepath.Join(home, "workspace"))
	mustMkdir(t, filepath.Join(home, "instances"))
	mustMkdir(t, filepath.Join(home, "instances", "abc"))
	mustMkdir(t, filepath.Join(home, "instances", "abc", "sessions"))
	mustMkdir(t, filepath.Join(home, "instances", "abc", "sessions", "s1"))

	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}

	ctx := Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances", "abc"),
		SessionDir:  filepath.Join(home, "instances", "abc", "sessions", "s1"),
		SocketDir:   t.TempDir(),
		Tools:       map[string]bool{"CreatePersistentInstance": true},
		Env:         func(string) string { return "" },
	}

	paths := p.Compile(ctx)
	agents := filepath.Join(home, "agents")
	if !slices.Contains(paths.ReadWrite, agents) {
		t.Errorf("agents not in RW list: rw=%v ro=%v", paths.ReadWrite, paths.ReadOnly)
	}
	if slices.Contains(paths.ReadOnly, agents) {
		t.Errorf("agents should have been promoted, still in RO list")
	}
}

func TestCompileAgentsRemainsRO(t *testing.T) {
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "agents"))
	mustMkdir(t, filepath.Join(home, "skills"))
	mustMkdir(t, filepath.Join(home, "workspace"))
	mustMkdir(t, filepath.Join(home, "instances", "abc", "sessions", "s1"))

	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances", "abc"),
		SessionDir:  filepath.Join(home, "instances", "abc", "sessions", "s1"),
		SocketDir:   t.TempDir(),
		Tools:       map[string]bool{"Bash": true},
		Env:         func(string) string { return "" },
	}
	paths := p.Compile(ctx)
	agents := filepath.Join(home, "agents")
	if slices.Contains(paths.ReadWrite, agents) {
		t.Errorf("agents unexpectedly RW without CreatePersistentInstance: %v", paths.ReadWrite)
	}
	if !slices.Contains(paths.ReadOnly, agents) {
		t.Errorf("agents should be RO: %v", paths.ReadOnly)
	}
}

func TestCompileDropsMissingPaths(t *testing.T) {
	home := t.TempDir() // no subdirs created
	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Home:        home,
		InstanceDir: home, // exists
		SessionDir:  home,
		SocketDir:   home,
		Tools:       map[string]bool{},
		Env:         func(string) string { return "" },
	}
	paths := p.Compile(ctx)
	for _, path := range paths.ReadWrite {
		if !pathExists(path) {
			t.Errorf("RW path does not exist: %s", path)
		}
	}
	for _, path := range paths.ReadOnly {
		if !pathExists(path) {
			t.Errorf("RO path does not exist: %s", path)
		}
	}
}

func TestCompileBashAddsTmp(t *testing.T) {
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "instances", "abc", "sessions", "s1"))

	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances", "abc"),
		SessionDir:  filepath.Join(home, "instances", "abc", "sessions", "s1"),
		SocketDir:   t.TempDir(),
		Tools:       map[string]bool{"Bash": true},
		Env:         func(string) string { return "" },
	}
	paths := p.Compile(ctx)
	if !slices.Contains(paths.ReadWrite, "/tmp") {
		t.Errorf("Bash agent missing /tmp: %v", paths.ReadWrite)
	}

	ctx.Tools = map[string]bool{}
	paths = p.Compile(ctx)
	if slices.Contains(paths.ReadWrite, "/tmp") {
		t.Errorf("non-Bash agent should not have /tmp: %v", paths.ReadWrite)
	}
}

func TestCompileIncludesMountsParent(t *testing.T) {
	// Host-exposed mounts are covered by the $HOME/mounts grant in the default
	// policy. Per-mount RW/RO is not enforced here — that's the mount layer's
	// job (Docker :ro, NFS ro, FUSE ro). The fspolicy compiler just needs to
	// include the parent.
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "mounts"))
	mustMkdir(t, filepath.Join(home, "instances", "abc", "sessions", "s1"))

	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	paths := p.Compile(Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances", "abc"),
		SessionDir:  filepath.Join(home, "instances", "abc", "sessions", "s1"),
		SocketDir:   t.TempDir(),
		Tools:       map[string]bool{},
		Env:         func(string) string { return "" },
	})
	mountsDir := filepath.Join(home, "mounts")
	if !slices.Contains(paths.ReadWrite, mountsDir) {
		t.Errorf("$HOME/mounts missing from RW Landlock paths: rw=%v", paths.ReadWrite)
	}
}

func TestReadableRootsExcludesSystemPathsAndPlatformInternals(t *testing.T) {
	// config/ and db/ exist on disk but MUST NOT appear in any roots — they
	// are deliberately absent from the policy. Creating them ensures this
	// test would catch a regression that adds $HOME/config or $HOME/db to
	// base.rw; if they didn't exist, the pathExists filter would hide the
	// regression and the assertion would pass tautologically.
	home := t.TempDir()
	for _, sub := range []string{"instances", "workspace", "mounts", "agents", "skills", ".ssh", ".config", "config", "db"} {
		mustMkdir(t, filepath.Join(home, sub))
	}
	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances"),
		SessionDir:  filepath.Join(home, "instances"),
		SocketDir:   t.TempDir(),
		Tools:       map[string]bool{"Bash": true},
		Env:         func(string) string { return "" },
	}

	readable := p.ReadableRoots(ctx)
	writable := p.WritableRoots(ctx)
	landlock := p.Compile(ctx)

	for _, r := range readable {
		if r == "/usr" || r == "/lib" || r == "/etc" || r == "/tmp" {
			t.Errorf("system path %s leaked into readable roots", r)
		}
	}

	configDir := filepath.Join(home, "config")
	dbDir := filepath.Join(home, "db")
	for _, list := range []struct {
		name  string
		paths []string
	}{
		{"readable roots", readable},
		{"writable roots", writable},
		{"landlock rw", landlock.ReadWrite},
		{"landlock ro", landlock.ReadOnly},
	} {
		if slices.Contains(list.paths, configDir) {
			t.Errorf("config/ leaked into %s: %v", list.name, list.paths)
		}
		if slices.Contains(list.paths, dbDir) {
			t.Errorf("db/ leaked into %s: %v", list.name, list.paths)
		}
	}
}

func TestNonBashAgentCannotReachCredentialPaths(t *testing.T) {
	// Core security claim of the on_tool.Bash gate: a file-only agent (no
	// Bash) cannot read or write ~/.ssh, ~/.gitconfig, ~/.config, ~/.cache,
	// or ~/.local. If anyone moves these paths back into base.rw, this test
	// catches it.
	home := t.TempDir()
	for _, sub := range []string{"instances", "workspace", ".ssh", ".config", ".cache", ".local"} {
		mustMkdir(t, filepath.Join(home, sub))
	}
	// .gitconfig is a file, not a dir — touch it so pathExists sees it.
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances"),
		SessionDir:  filepath.Join(home, "instances"),
		SocketDir:   t.TempDir(),
		Tools:       map[string]bool{}, // no Bash
		Env:         func(string) string { return "" },
	}

	readable := p.ReadableRoots(ctx)
	writable := p.WritableRoots(ctx)
	landlock := p.Compile(ctx)

	credentialPaths := []string{".ssh", ".config", ".gitconfig", ".cache", ".local"}
	for _, name := range credentialPaths {
		path := filepath.Join(home, name)
		if slices.Contains(readable, path) {
			t.Errorf("non-Bash agent can read %s", name)
		}
		if slices.Contains(writable, path) {
			t.Errorf("non-Bash agent can write %s", name)
		}
		if slices.Contains(landlock.ReadWrite, path) || slices.Contains(landlock.ReadOnly, path) {
			t.Errorf("non-Bash agent has Landlock grant for %s", name)
		}
	}

	// Sanity check: the same paths SHOULD appear for a Bash-enabled agent.
	ctx.Tools = map[string]bool{"Bash": true}
	bashReadable := p.ReadableRoots(ctx)
	for _, name := range credentialPaths {
		path := filepath.Join(home, name)
		if !slices.Contains(bashReadable, path) {
			t.Errorf("Bash agent missing credential path %s — the test's negative-side is meaningless without this positive-side check", name)
		}
	}
}

func TestWritableRootsExcludesROPaths(t *testing.T) {
	home := t.TempDir()
	for _, sub := range []string{"instances", "workspace", "agents", "skills", ".ssh", ".config", "mounts"} {
		mustMkdir(t, filepath.Join(home, sub))
	}
	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}

	// An agent without CreatePersistentInstance must not get agents/ or skills/
	// in its writable roots. agents/ is in base.ro; that's read-only.
	ctx := Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances"),
		SessionDir:  filepath.Join(home, "instances"),
		SocketDir:   t.TempDir(),
		Tools:       map[string]bool{"Bash": true},
		Env:         func(string) string { return "" },
	}
	writable := p.WritableRoots(ctx)
	agents := filepath.Join(home, "agents")
	if slices.Contains(writable, agents) {
		t.Errorf("agents/ should not be writable without CreatePersistentInstance: %v", writable)
	}
	readable := p.ReadableRoots(ctx)
	if !slices.Contains(readable, agents) {
		t.Errorf("agents/ should be readable (it's base.ro): %v", readable)
	}

	// With CreatePersistentInstance, agents/ is promoted to RW and should
	// appear in both lists.
	ctx.Tools = map[string]bool{"CreatePersistentInstance": true}
	writable = p.WritableRoots(ctx)
	if !slices.Contains(writable, agents) {
		t.Errorf("agents/ should be writable with CreatePersistentInstance: %v", writable)
	}
}

func TestRootsIncludeMountsParent(t *testing.T) {
	// The $HOME/mounts parent is granted RW; individual mount RO is the
	// mount layer's responsibility. Both readable and writable roots include
	// the parent so the file-tool guard lets agents reach mounts at all.
	home := t.TempDir()
	for _, sub := range []string{"instances", "mounts"} {
		mustMkdir(t, filepath.Join(home, sub))
	}
	p, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Home:        home,
		InstanceDir: filepath.Join(home, "instances"),
		SessionDir:  filepath.Join(home, "instances"),
		SocketDir:   t.TempDir(),
		Env:         func(string) string { return "" },
	}

	mountsDir := filepath.Join(home, "mounts")
	if !slices.Contains(p.ReadableRoots(ctx), mountsDir) {
		t.Errorf("$HOME/mounts missing from readable roots")
	}
	if !slices.Contains(p.WritableRoots(ctx), mountsDir) {
		t.Errorf("$HOME/mounts missing from writable roots")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0755); err != nil {
		t.Fatal(err)
	}
}
