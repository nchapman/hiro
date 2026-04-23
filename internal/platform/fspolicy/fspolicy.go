// Package fspolicy parses and compiles the declarative filesystem policy that
// governs worker Landlock rulesets and the in-process file-tool guard.
//
// The policy ships as an embedded default (default.yaml) and may be overridden
// by operators via the filesystem: key in config/config.yaml. The Policy type
// is owned and persisted by the controlplane package; this package is pure
// parser + compiler with no file I/O.
//
// The policy has three sections:
//
//   - base      paths granted to every worker (rw or ro)
//   - on_tool   paths granted when the agent has a named tool (rw wins over
//     a lower base setting — this is how operator agents get write
//     access to agents/ while others see it read-only)
//   - per_instance  dynamic paths expanded per spawn ($INSTANCE_DIR, etc.)
//
// Paths absent from the policy are implicitly blocked by Landlock's
// default-deny model. config/ and db/ are the load-bearing examples: the
// policy file lives inside config/, so an agent cannot rewrite policy to
// widen its own access.
//
// Variable expansion uses $NAME syntax (no braces). $HOME, $INSTANCE_DIR,
// $SESSION_DIR, $SOCKET_DIR are provided by the compiler; any other variable
// resolves via Context.Env (typically os.Getenv). A variable that resolves
// to an empty string drops the whole path — that's how optional
// env-configured locations like $MISE_DATA_DIR stay silent on systems that
// don't have them.
//
// Compile drops paths that don't exist on disk. The Landlock syscall opens
// each path with O_PATH, so a missing entry would fail ruleset creation;
// dropping silently keeps the policy declarative.
package fspolicy

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nchapman/hiro/internal/ipc"
)

//go:embed default.yaml
var defaultYAML []byte

// Default returns the embedded default policy bytes.
func Default() []byte { return defaultYAML }

// Policy is the parsed YAML document.
type Policy struct {
	Version     int                  `yaml:"version"`
	Base        AccessSet            `yaml:"base"`
	OnTool      map[string]AccessSet `yaml:"on_tool"`
	PerInstance AccessSet            `yaml:"per_instance"`
}

// AccessSet is a pair of RW/RO path lists.
type AccessSet struct {
	RW []string `yaml:"rw"`
	RO []string `yaml:"ro"`
}

// Context carries the spawn-time variables and the agent's effective toolset.
// Env resolves non-built-in variables (e.g. $MISE_DATA_DIR); if nil, os.Getenv
// is used.
//
// Host-exposed mounts under $HOME/mounts are not listed here — the parent
// directory is granted via base.rw in the default policy, and per-mount
// read-only enforcement happens at the mount layer (Docker :ro, NFS mount
// options, FUSE read-only exports). Landlock does not second-guess the mount
// flags.
type Context struct {
	Home        string
	InstanceDir string
	SessionDir  string
	SocketDir   string
	Tools       map[string]bool
	Env         func(string) string
}

// Parse parses policy bytes.
func Parse(data []byte) (*Policy, error) {
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse filesystem policy: %w", err)
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if p.Version != 1 {
		return nil, fmt.Errorf("unsupported filesystem policy version: %d", p.Version)
	}
	return &p, nil
}

// Compile resolves the policy against the given context and returns the
// Landlock paths to apply. Non-existent paths are dropped silently; the
// Landlock syscall opens each path with O_PATH and would fail otherwise.
//
// When a path appears at both RW and RO, RW wins.
func (p *Policy) Compile(ctx Context) ipc.LandlockPaths {
	vars, env := compileVars(ctx)
	modes := make(map[string]int) // path → 0 ro, 1 rw

	addSet(modes, p.Base, vars, env)
	for tool := range ctx.Tools {
		if rules, ok := p.OnTool[tool]; ok {
			addSet(modes, rules, vars, env)
		}
	}
	addSet(modes, p.PerInstance, vars, env)

	var rw, ro []string
	for path, mode := range modes {
		if !pathExists(path) {
			continue
		}
		if mode == 1 {
			rw = append(rw, path)
		} else {
			ro = append(ro, path)
		}
	}
	sort.Strings(rw)
	sort.Strings(ro)
	return ipc.LandlockPaths{ReadWrite: rw, ReadOnly: ro}
}

// compileVars builds the variable table and env resolver for a spawn context.
func compileVars(ctx Context) (map[string]string, func(string) string) {
	env := ctx.Env
	if env == nil {
		env = os.Getenv
	}
	return map[string]string{
		"HOME":         ctx.Home,
		"INSTANCE_DIR": ctx.InstanceDir,
		"SESSION_DIR":  ctx.SessionDir,
		"SOCKET_DIR":   ctx.SocketDir,
	}, env
}

// addSet merges an AccessSet into modes. RW always wins; RO only sets a mode
// when no prior entry exists.
func addSet(modes map[string]int, set AccessSet, vars map[string]string, env func(string) string) {
	for _, raw := range set.RO {
		path, ok := expand(raw, vars, env)
		if !ok {
			continue
		}
		if _, present := modes[path]; !present {
			modes[path] = 0
		}
	}
	for _, raw := range set.RW {
		path, ok := expand(raw, vars, env)
		if !ok {
			continue
		}
		modes[path] = 1
	}
}

// ReadableRoots returns paths that Read, Glob, and Grep may address. This is
// the RW set plus the RO set (both include reads), restricted to subpaths of
// $HOME. System paths (/usr, /etc) are excluded — exec'd commands find them
// on PATH but agents can't browse them through the file tools. Agents/
// and skills/ are included here via base.ro so agents can inspect
// definitions even without CreatePersistentInstance.
func (p *Policy) ReadableRoots(ctx Context) []string {
	return p.homeSubtreeRoots(ctx, true)
}

// WritableRoots returns paths that Write and Edit may address. RW-only. An
// agent without CreatePersistentInstance sees agents/ in ReadableRoots (it's
// base.ro) but not here, so it can inspect agent definitions through Read
// but can't rewrite them through Write/Edit.
func (p *Policy) WritableRoots(ctx Context) []string {
	return p.homeSubtreeRoots(ctx, false)
}

// homeSubtreeRoots returns the unique set of policy paths under $HOME. When
// includeRO is true, base.ro paths are included (for Read/Glob/Grep); when
// false, only RW paths are returned (for Write/Edit).
func (p *Policy) homeSubtreeRoots(ctx Context, includeRO bool) []string {
	vars, env := compileVars(ctx)

	seen := make(map[string]struct{})
	prefix := ctx.Home + string(filepath.Separator)
	collect := func(list []string) {
		for _, raw := range list {
			path, ok := expand(raw, vars, env)
			if !ok {
				continue
			}
			// Separator-anchored match: /home/hiropoison must not pass when
			// ctx.Home is /home/hiro. Also exclude ctx.Home itself so a bare
			// $HOME entry can't implicitly grant access to config/ and db/.
			if path == ctx.Home || !strings.HasPrefix(path, prefix) {
				continue
			}
			seen[path] = struct{}{}
		}
	}

	collect(p.Base.RW)
	if includeRO {
		collect(p.Base.RO)
	}
	for tool := range ctx.Tools {
		if rules, ok := p.OnTool[tool]; ok {
			collect(rules.RW)
			if includeRO {
				collect(rules.RO)
			}
		}
	}
	collect(p.PerInstance.RW)
	if includeRO {
		collect(p.PerInstance.RO)
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// expand resolves $VAR references in a path. Returns ok=false if any variable
// is unknown or expands to empty, if the result is not absolute, or if the
// raw (pre-Clean) path contains traversal components. Rejecting `..` in the
// source text prevents a misconfigured policy entry like `$HOME/../etc` from
// silently producing `/etc` as a grant.
//
// Substitution is single-pass: expanded values are NOT re-scanned for $VAR
// tokens. This matters because variables can resolve from os.Getenv (e.g.
// $MISE_DATA_DIR), so a malicious or misconfigured env like
// MISE_DATA_DIR='$MISE_DATA_DIR/x' would loop forever under recursive scans.
func expand(raw string, vars map[string]string, env func(string) string) (string, bool) {
	var b strings.Builder
	b.Grow(len(raw))
	i := 0
	for i < len(raw) {
		if raw[i] != '$' {
			b.WriteByte(raw[i])
			i++
			continue
		}
		j := i + 1
		for j < len(raw) && isVarChar(raw[j]) {
			j++
		}
		name := raw[i+1 : j]
		if name == "" {
			return "", false
		}
		val, ok := vars[name]
		if !ok {
			val = env(name)
		}
		if val == "" {
			return "", false
		}
		b.WriteString(val)
		i = j
	}
	out := b.String()
	if !filepath.IsAbs(out) {
		return "", false
	}
	// Reject traversal components so `$HOME/../etc/passwd` can't escape.
	if slices.Contains(strings.Split(out, string(filepath.Separator)), "..") {
		return "", false
	}
	return filepath.Clean(out), true
}

func isVarChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
