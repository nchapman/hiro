package toolrules

import (
	"path/filepath"
	"strings"
)

// Decision is the result of evaluating a tool call against rules.
type Decision int

const (
	// Allowed means an allow rule explicitly matched the call.
	Allowed Decision = iota
	// Denied means a deny rule matched, or allow rules exist but none matched.
	Denied
	// Unmatched means no rules referenced this tool at all.
	Unmatched
	// NeedsReview means the call is too complex for static analysis.
	// The caller should deny the call or escalate to an LLM for review.
	NeedsReview
)

func (d Decision) String() string {
	switch d {
	case Allowed:
		return "allowed"
	case Denied:
		return "denied"
	case NeedsReview:
		return "needs_review"
	default:
		return "unmatched"
	}
}

// MatchResult is the outcome of matching a pattern against a value.
type MatchResult int

const (
	// NoMatch means the pattern does not match the value.
	NoMatch MatchResult = iota
	// Match means the pattern matches the value.
	Match
	// Uncertain means the value is too complex to determine statically.
	Uncertain
)

// ParamExtractor returns the matchable string value from tool call parameters.
type ParamExtractor func(params map[string]any) string

// MatchFunc determines whether a rule pattern matches an extracted
// parameter value. Returns Match, NoMatch, or Uncertain.
type MatchFunc func(pattern, value string) MatchResult

// defaultExtractors maps each built-in tool to the parameter that
// parameterized rules match against.
// strExtractor returns a ParamExtractor that reads a string key from params.
func strExtractor(key string) ParamExtractor {
	return func(p map[string]any) string {
		s, _ := p[key].(string)
		return s
	}
}

// pathExtractor returns a ParamExtractor that reads a file path key and
// normalizes it with filepath.Clean to prevent traversal bypasses.
func pathExtractor(key string) ParamExtractor {
	return func(p map[string]any) string {
		s, _ := p[key].(string)
		if s == "" {
			return ""
		}
		return filepath.Clean(s)
	}
}

var defaultExtractors = map[string]ParamExtractor{
	"Bash":          strExtractor("command"),
	"Read":          pathExtractor("file_path"),
	"Write":         pathExtractor("file_path"),
	"Edit":          pathExtractor("file_path"),
	"Glob":          pathExtractor("pattern"),
	"Grep":          pathExtractor("pattern"),
	"WebFetch":      strExtractor("url"),
	"SpawnInstance": strExtractor("agent"),
	"TaskOutput":    strExtractor("task_id"),
	"TaskStop":      strExtractor("task_id"),
}

// defaultMatchers overrides the default wildcard matching for tools
// that need special matching semantics.
var defaultMatchers = map[string]MatchFunc{
	"Bash":          bashMatch,
	"SpawnInstance": matchCommaList,
}

// wildcardMatchFunc adapts MatchWildcard (bool) to the MatchFunc interface.
func wildcardMatchFunc(pattern, value string) MatchResult {
	if MatchWildcard(pattern, value) {
		return Match
	}
	return NoMatch
}

// matchCommaList treats the pattern as a comma-separated list of
// wildcard patterns and returns Match if the value matches any of them.
// Used for SpawnInstance(worker,researcher) style rules where each
// item can also be a wildcard pattern (e.g. "research*,coder").
func matchCommaList(pattern, value string) MatchResult {
	for item := range strings.SplitSeq(pattern, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue // skip empty items from trailing/leading/double commas
		}
		if MatchWildcard(item, value) {
			return Match
		}
	}
	return NoMatch
}

// Checker evaluates tool calls against allow and deny rules.
//
// The zero value is ready to use with default extractors and matchers
// for all built-in tools.
type Checker struct {
	// Extractors maps tool names to parameter extractors.
	// Custom extractors are checked first; built-in defaults are used
	// as a fallback.
	Extractors map[string]ParamExtractor

	// Matchers maps tool names to custom match functions. Custom
	// matchers are checked first; built-in defaults (bashMatch for
	// Bash, matchCommaList for SpawnInstance, wildcardMatchFunc for
	// everything else) are used as a fallback.
	Matchers map[string]MatchFunc
}

// Check evaluates whether a tool call is permitted.
//
// Evaluation order:
//  1. For each deny rule: if Match → Denied. If Uncertain → note it.
//  2. If any deny rule was uncertain → NeedsReview.
//  3. For each allow rule: if Match → note it. If Uncertain → note it.
//  4. If any allow rule was uncertain → NeedsReview.
//  5. If allow matched with no uncertainty → Allowed.
//  6. If allow rules exist but none matched → Denied.
//  7. Unmatched.
//
// Deny always wins over allow. Uncertainty on deny rules is fail-closed
// (NeedsReview rather than allowing).
func (c *Checker) Check(tool string, params map[string]any, allow, deny []Rule) Decision {
	// Phase 1: deny rules. A definite match is immediately Denied.
	denyUncertain := false
	for i := range deny {
		if deny[i].Tool != tool {
			continue
		}
		switch c.matchResult(&deny[i], tool, params) {
		case Match:
			return Denied
		case Uncertain:
			denyUncertain = true
		}
	}

	// Uncertainty on any deny rule → fail-closed.
	if denyUncertain {
		return NeedsReview
	}

	// Phase 2: allow rules.
	hasAllow := false
	allowMatched := false
	allowUncertain := false
	for i := range allow {
		if allow[i].Tool != tool {
			continue
		}
		hasAllow = true
		switch c.matchResult(&allow[i], tool, params) {
		case Match:
			allowMatched = true
		case Uncertain:
			allowUncertain = true
		}
	}

	if allowUncertain {
		return NeedsReview
	}
	if allowMatched {
		return Allowed
	}
	if hasAllow {
		return Denied // allow rules exist but none matched
	}
	return Unmatched
}

// matchResult reports whether a single rule matches the given tool call.
func (c *Checker) matchResult(r *Rule, tool string, params map[string]any) MatchResult {
	if r.Tool != tool {
		return NoMatch
	}
	if r.IsWholeTool() {
		return Match
	}

	// Resolve extractor: custom → default.
	var fn ParamExtractor
	if c.Extractors != nil {
		fn = c.Extractors[tool]
	}
	if fn == nil {
		fn = defaultExtractors[tool]
	}
	if fn == nil {
		return NoMatch // no extractor → parameterized rules can't match
	}

	return c.matcher(tool)(r.Pattern, fn(params))
}

// matcher returns the match function for the given tool.
func (c *Checker) matcher(tool string) MatchFunc {
	if c.Matchers != nil {
		if fn := c.Matchers[tool]; fn != nil {
			return fn
		}
	}
	if fn := defaultMatchers[tool]; fn != nil {
		return fn
	}
	return wildcardMatchFunc
}
