package toolrules

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// dangerousBuiltins are commands that can execute arbitrary code which
// cannot be statically analyzed. Their presence makes the command
// Uncertain — the caller should escalate to an LLM for review.
var dangerousBuiltins = map[string]bool{
	// Shell code evaluation.
	"eval":   true,
	"exec":   true,
	"source": true,
	".":      true, // source alias

	// Shells — can run arbitrary commands via -c flag.
	"bash": true,
	"sh":   true,
	"zsh":  true,
	"dash": true,

	// Command wrappers — execute another command as their argument.
	"env":     true,
	"xargs":   true,
	"nohup":   true,
	"nice":    true,
	"sudo":    true,
	"su":      true,
	"doas":    true,
	"command": true, // bypasses function/alias lookup
	"builtin": true, // bypasses function lookup

	// Script interpreters — can execute arbitrary code via -c/-e flags.
	"python":  true,
	"python3": true,
	"python2": true,
	"perl":    true,
	"ruby":    true,
	"node":    true,
	"php":     true,
	"lua":     true,

	// Signal/exit hooks — trap body is shell code, EXIT fires at shell exit.
	"trap": true,
}

// bashMatch is the MatchFunc for Bash tool rules. It parses the command
// with a real shell parser, extracts simple commands from all nesting
// levels (including inside $() and backticks), and matches each against
// the pattern.
//
// Returns Uncertain for commands containing dangerous builtins, ANSI-C
// quoting, variable expansion in command position, or parse errors.
func bashMatch(pattern, command string) MatchResult {
	cmds, uncertain := extractCommands(command)
	for _, cmd := range cmds {
		if MatchWildcard(pattern, cmd) {
			return Match
		}
	}
	if uncertain {
		return Uncertain
	}
	return NoMatch
}

// extractCommands parses a bash command string using mvdan.cc/sh and
// returns all simple commands found at every nesting level, plus whether
// the command contains constructs that prevent reliable static analysis.
func extractCommands(command string) (cmds []string, uncertain bool) {
	parser := syntax.NewParser(
		syntax.Variant(syntax.LangBash),
		syntax.KeepComments(false),
	)
	f, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, true // parse error → uncertain
	}

	syntax.Walk(f, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CallExpr:
			cmd, hasOpaque := callExprToString(n)
			if cmd == "" {
				return true
			}
			if hasOpaque {
				uncertain = true
			}
			// Check for dangerous builtins.
			name := firstWord(n)
			if dangerousBuiltins[name] {
				uncertain = true
			}
			// Variable expansion in command-name position — the actual
			// command is unknowable statically.
			if len(n.Args) > 0 && wordHasParamExp(n.Args[0]) {
				uncertain = true
			}
			cmds = append(cmds, cmd)
			// Continue descending into args — they may contain CmdSubst
			// or ProcSubst nodes with additional commands inside.
			return true

		case *syntax.CmdSubst:
			// The substitution's runtime output is unknowable statically,
			// so mark uncertain. We still descend so that deny rules can
			// match commands inside $() — e.g. deny(rm) fires for
			// "echo $(rm /)".
			uncertain = true
			return true

		case *syntax.ProcSubst:
			uncertain = true
			return true
		}
		return true
	})

	return cmds, uncertain
}

// callExprToString reconstructs a simple command from a CallExpr node,
// joining the command name and arguments with spaces. Returns the
// reconstructed string and whether any opaque constructs (ANSI-C
// quoting, unexpandable expressions) were encountered.
func callExprToString(ce *syntax.CallExpr) (string, bool) {
	if len(ce.Args) == 0 {
		return "", false
	}
	hasOpaque := false
	parts := make([]string, 0, len(ce.Args))
	for _, w := range ce.Args {
		s, opaque := wordToString(w)
		if opaque {
			hasOpaque = true
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	result := strings.Join(parts, " ")
	if strings.TrimSpace(result) == "" {
		return "", hasOpaque
	}
	return result, hasOpaque
}

// firstWord returns the literal text of the first argument (the command
// name) of a CallExpr, or empty string if unavailable.
func firstWord(ce *syntax.CallExpr) string {
	if len(ce.Args) == 0 {
		return ""
	}
	s, _ := wordToString(ce.Args[0])
	return s
}

// wordHasParamExp reports whether a Word contains any variable expansion
// ($VAR, ${VAR}). Used to detect dynamic command names.
func wordHasParamExp(w *syntax.Word) bool {
	for _, part := range w.Parts {
		if _, ok := part.(*syntax.ParamExp); ok {
			return true
		}
	}
	return false
}

// wordToString reconstructs a Word AST node into its string value,
// stripping quote characters but preserving content. Returns the string
// and whether any opaque constructs were encountered (ANSI-C quoting,
// unexpandable expressions).
func wordToString(w *syntax.Word) (string, bool) {
	var sb strings.Builder
	opaque := false
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				// ANSI-C quoting ($'...') expands escape sequences at
				// runtime (\x72\x6d → rm). Cannot decode statically.
				opaque = true
				sb.WriteString("$'...'")
			} else {
				sb.WriteString(p.Value)
			}
		case *syntax.DblQuoted:
			for _, dp := range p.Parts {
				switch inner := dp.(type) {
				case *syntax.Lit:
					sb.WriteString(inner.Value)
				case *syntax.ParamExp:
					sb.WriteByte('$')
					if inner.Param != nil {
						sb.WriteString(inner.Param.Value)
					}
				default:
					// CmdSubst inside quotes, etc.
					opaque = true
					sb.WriteString("$(...)")
				}
			}
		case *syntax.ParamExp:
			sb.WriteByte('$')
			if p.Param != nil {
				sb.WriteString(p.Param.Value)
			}
		default:
			// ArithmExp, CmdSubst as standalone arg, etc.
			opaque = true
			sb.WriteString("$(...)")
		}
	}
	return sb.String(), opaque
}
