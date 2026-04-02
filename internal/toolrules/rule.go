// Package toolrules provides parameterized tool permission rules.
//
// Rules use the format "Tool(pattern)" to restrict tool usage at the
// parameter level. For example, "Bash(curl *)" permits only Bash
// commands matching "curl *". A rule without parentheses (e.g. "Bash")
// applies to the entire tool unconditionally.
package toolrules

import (
	"fmt"
	"strings"
)

// Rule is a parsed tool permission rule.
type Rule struct {
	Tool    string // tool name, e.g. "Bash"
	Pattern string // wildcard pattern; empty for whole-tool rules
}

// ParseRule parses a rule string like "Bash(curl *)" into a Rule.
// Whole-tool rules like "Bash" have an empty Pattern.
func ParseRule(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rule{}, fmt.Errorf("empty rule")
	}

	idx := strings.IndexByte(s, '(')
	if idx < 0 {
		return Rule{Tool: s}, nil
	}

	if s[len(s)-1] != ')' {
		return Rule{}, fmt.Errorf("unclosed parenthesis in rule %q", s)
	}

	tool := strings.TrimSpace(s[:idx])
	if tool == "" {
		return Rule{}, fmt.Errorf("empty tool name in rule %q", s)
	}

	pattern := s[idx+1 : len(s)-1]
	if pattern == "" {
		return Rule{}, fmt.Errorf("empty pattern in rule %q; omit parentheses for whole-tool rules", s)
	}

	return Rule{Tool: tool, Pattern: pattern}, nil
}

// ParseRules parses multiple rule strings. Returns an error if any
// individual rule is malformed.
func ParseRules(ss []string) ([]Rule, error) {
	rules := make([]Rule, 0, len(ss))
	for _, s := range ss {
		r, err := ParseRule(s)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// String formats the rule as its canonical string representation.
func (r Rule) String() string {
	if r.Pattern == "" {
		return r.Tool
	}
	return r.Tool + "(" + r.Pattern + ")"
}

// IsWholeTool reports whether this rule applies to the entire tool
// without parameter-level restrictions.
func (r Rule) IsWholeTool() bool {
	return r.Pattern == ""
}
