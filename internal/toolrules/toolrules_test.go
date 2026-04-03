package toolrules

import (
	"testing"
)

// --- Rule parsing ---

func TestParseRule(t *testing.T) {
	tests := []struct {
		input   string
		want    Rule
		wantErr bool
	}{
		// Whole-tool rules.
		{"Bash", Rule{Tool: "Bash"}, false},
		{"Read", Rule{Tool: "Read"}, false},
		{"  Bash  ", Rule{Tool: "Bash"}, false},

		// Parameterized rules.
		{"Bash(curl *)", Rule{Tool: "Bash", Pattern: "curl *"}, false},
		{"Read(/src/*.go)", Rule{Tool: "Read", Pattern: "/src/*.go"}, false},
		{"Agent(worker,researcher)", Rule{Tool: "Agent", Pattern: "worker,researcher"}, false},
		{"WebFetch(https://api.example.com/*)", Rule{Tool: "WebFetch", Pattern: "https://api.example.com/*"}, false},
		{"Bash(echo (hello))", Rule{Tool: "Bash", Pattern: "echo (hello)"}, false},
		{"Bash (curl *)", Rule{Tool: "Bash", Pattern: "curl *"}, false},

		// Errors.
		{"", Rule{}, true},
		{"(foo)", Rule{}, true},
		{"Bash(curl", Rule{}, true},
		{"  ", Rule{}, true},
		{"Bash()", Rule{}, true},
		{"Bash(curl *)extra", Rule{}, true},
	}

	for _, tt := range tests {
		got, err := ParseRule(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseRule(%q) = %v, want error", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRule(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseRule(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseRules(t *testing.T) {
	rules, err := ParseRules([]string{"Bash", "Read(/src/*)", "WebFetch(https://*)"})
	if err != nil {
		t.Fatalf("ParseRules error: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(rules))
	}
	if rules[1].Pattern != "/src/*" {
		t.Errorf("rules[1].Pattern = %q, want /src/*", rules[1].Pattern)
	}
}

func TestParseRules_Error(t *testing.T) {
	_, err := ParseRules([]string{"Bash", "Bad(unclosed"})
	if err == nil {
		t.Error("ParseRules should fail on malformed rule")
	}
}

func TestParseRules_Nil(t *testing.T) {
	rules, err := ParseRules(nil)
	if err != nil {
		t.Fatalf("ParseRules(nil) error: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("ParseRules(nil) returned %d rules, want 0", len(rules))
	}
}

func TestRuleString(t *testing.T) {
	tests := []struct {
		rule Rule
		want string
	}{
		{Rule{Tool: "Bash"}, "Bash"},
		{Rule{Tool: "Bash", Pattern: "curl *"}, "Bash(curl *)"},
		{Rule{Tool: "Read", Pattern: "/src/*.go"}, "Read(/src/*.go)"},
	}
	for _, tt := range tests {
		if got := tt.rule.String(); got != tt.want {
			t.Errorf("%v.String() = %q, want %q", tt.rule, got, tt.want)
		}
	}
}

func TestRuleIsWholeTool(t *testing.T) {
	if !(Rule{Tool: "Bash"}).IsWholeTool() {
		t.Error("Rule without pattern should be whole-tool")
	}
	if (Rule{Tool: "Bash", Pattern: "curl *"}).IsWholeTool() {
		t.Error("Rule with pattern should not be whole-tool")
	}
}

func TestParseRoundTrip(t *testing.T) {
	inputs := []string{
		"Bash",
		"Bash(curl *)",
		"Read(/src/*/main.go)",
		"Agent(worker,researcher)",
	}
	for _, s := range inputs {
		r, err := ParseRule(s)
		if err != nil {
			t.Fatalf("ParseRule(%q): %v", s, err)
		}
		if got := r.String(); got != s {
			t.Errorf("round-trip: %q → %v → %q", s, r, got)
		}
	}
}

// --- Wildcard matching ---

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		pattern string
		text    string
		want    bool
	}{
		// Exact matches.
		{"hello", "hello", true},
		{"hello", "world", false},
		{"", "", true},
		{"", "nonempty", false},

		// Single wildcard.
		{"*", "", true},
		{"*", "anything", true},
		{"*", "multiple words here", true},

		// Prefix wildcard.
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", true},
		{"*.go", "main.rs", false},

		// Suffix wildcard.
		{"curl *", "curl https://example.com", true},
		{"curl *", "wget foo", false},
		{"rm *", "rm -rf /", true},

		// Trailing wildcard is optional (sole wildcard).
		{"curl *", "curl", true},
		{"git *", "git", true},
		{"git *", "git status", true},
		{"* *", "noSpaces", false}, // multiple wildcards — no optional

		// Middle wildcard.
		{"src/*/main.go", "src/app/main.go", true},
		{"src/*/main.go", "src/deep/nested/main.go", true},
		{"src/*/main.go", "src/main.go", false},

		// Multiple wildcards.
		{"*/*", "src/main.go", true},
		{"*/*", "noSlash", false},
		{"*.*", "file.txt", true},
		{"*.*", "noext", false},

		// Adjacent wildcards.
		{"**", "anything", true},
		{"a**b", "axyzb", true},
		{"a**b", "ab", true},
		{"***", "anything", true},
		{"***", "", true},

		// Trailing wildcard matches empty.
		{"hello*", "hello", true},
		{"hello*", "hello world", true},

		// Pattern longer than text, no wildcard.
		{"abcde", "abc", false},

		// Non-ASCII / multibyte characters.
		{"*/résumé.pdf", "uploads/résumé.pdf", true},
		{"*.txt", "файл.txt", true},
		{"café*", "café au lait", true},
		{"café*", "cafe au lait", false},

		// Path traversal — lexical matching only.
		{"/src/*", "/src/../etc/passwd", true},
		{"/src/*", "/src/../../etc/shadow", true},

		// Escape sequences.
		{`hello\*world`, "hello*world", true},
		{`hello\*world`, "helloXworld", false},
		{`a\\b`, `a\b`, true},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axyzb", false},
		{`path/\*.go`, "path/*.go", true},
		{`path/\*.go`, "path/main.go", false},
		{`end\*`, "end*", true},
		{`end\*`, "endXYZ", false},
		{`\\*`, `\anything`, true},
		{`\\\*`, `\*`, true},
		{`\\\*`, `\anything`, false},
		{`git \*`, "git", false},
		{`git \*`, "git *", true},
		{`*\*`, "", false},
		{`*\*`, "*", true},
		{`*\*`, "foo*", true},
		{`*\*`, "foo", false},
	}

	for _, tt := range tests {
		got := MatchWildcard(tt.pattern, tt.text)
		if got != tt.want {
			t.Errorf("MatchWildcard(%q, %q) = %v, want %v", tt.pattern, tt.text, got, tt.want)
		}
	}
}

// --- AST-based bash extraction ---

func TestExtractCommands(t *testing.T) {
	tests := []struct {
		cmd        string
		wantCmds   []string
		wantUncert bool
	}{
		// Simple command.
		{"ls -la", []string{"ls -la"}, false},

		// Compound commands.
		{"echo hello && rm -rf /", []string{"echo hello", "rm -rf /"}, false},
		{"cmd1 || cmd2", []string{"cmd1", "cmd2"}, false},
		{"cat file | grep pattern", []string{"cat file", "grep pattern"}, false},
		{"a; b; c", []string{"a", "b", "c"}, false},

		// Command substitution — extracts inner commands, marks uncertain.
		{"echo $(rm -rf /)", []string{"echo $(...)", "rm -rf /"}, true},
		{"echo `rm -rf /`", []string{"echo $(...)", "rm -rf /"}, true},

		// Process substitution — extracts inner commands.
		{"cat <(ls)", []string{"ls"}, true},

		// Subshell.
		{"(rm -rf /)", []string{"rm -rf /"}, false},

		// Dangerous builtins.
		{"eval 'rm -rf /'", []string{"eval rm -rf /"}, true},
		{"exec rm -rf /", []string{"exec rm -rf /"}, true},
		{"source script.sh", []string{"source script.sh"}, true},

		// Env var assignment + command.
		{"FOO=bar rm -rf /", []string{"rm -rf /"}, false},

		// Quoted arguments — quotes stripped.
		{`echo "hello world"`, []string{"echo hello world"}, false},
		{"echo 'hello world'", []string{"echo hello world"}, false},

		// Variable expansion.
		{"echo $HOME", []string{"echo $HOME"}, false},

		// Nested substitution.
		{"echo $(echo $(rm -rf /))", nil, true}, // uncertain, inner commands extracted
	}

	for _, tt := range tests {
		cmds, uncertain := extractCommands(tt.cmd)
		if uncertain != tt.wantUncert {
			t.Errorf("extractCommands(%q) uncertain = %v, want %v", tt.cmd, uncertain, tt.wantUncert)
		}
		if tt.wantCmds != nil && !sliceContainsAll(cmds, tt.wantCmds) {
			t.Errorf("extractCommands(%q) cmds = %v, want to contain %v", tt.cmd, cmds, tt.wantCmds)
		}
	}
}

func TestExtractCommands_ParseError(t *testing.T) {
	_, uncertain := extractCommands("if then fi ((")
	if !uncertain {
		t.Error("parse error should be uncertain")
	}
}

// --- Checker ---

func bashParams(cmd string) map[string]any {
	return map[string]any{"command": cmd}
}

func fileParams(path string) map[string]any {
	return map[string]any{"file_path": path}
}

func agentParams(name string) map[string]any {
	return map[string]any{"agent": name}
}

func TestChecker_WholeTool(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash"}}

	if d := c.Check("Bash", bashParams("anything"), allow, nil); d != Allowed {
		t.Errorf("whole-tool allow: got %v, want Allowed", d)
	}
	if d := c.Check("Read", fileParams("/etc/passwd"), allow, nil); d != Unmatched {
		t.Errorf("different tool: got %v, want Unmatched", d)
	}
}

func TestChecker_WholeToolDeny(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash"}}

	if d := c.Check("Bash", bashParams("anything"), nil, deny); d != Denied {
		t.Errorf("whole-tool deny: got %v, want Denied", d)
	}
	if d := c.Check("Read", fileParams("foo"), nil, deny); d != Unmatched {
		t.Errorf("different tool: got %v, want Unmatched", d)
	}
}

func TestChecker_ParameterizedAllow(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash", Pattern: "curl *"}}

	if d := c.Check("Bash", bashParams("curl https://example.com"), allow, nil); d != Allowed {
		t.Errorf("matching pattern: got %v, want Allowed", d)
	}
	if d := c.Check("Bash", bashParams("rm -rf /"), allow, nil); d != Denied {
		t.Errorf("non-matching pattern: got %v, want Denied", d)
	}
}

func TestChecker_ParameterizedDeny(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash"}}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	if d := c.Check("Bash", bashParams("rm -rf /"), allow, deny); d != Denied {
		t.Errorf("matching deny: got %v, want Denied", d)
	}
	if d := c.Check("Bash", bashParams("curl https://example.com"), allow, deny); d != Allowed {
		t.Errorf("non-matching deny: got %v, want Allowed", d)
	}
}

func TestChecker_DenyTakesPrecedence(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash", Pattern: "rm *"}}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	if d := c.Check("Bash", bashParams("rm -rf /"), allow, deny); d != Denied {
		t.Errorf("deny precedence: got %v, want Denied", d)
	}
}

func TestChecker_MultipleAllowPatterns(t *testing.T) {
	c := &Checker{}
	allow := []Rule{
		{Tool: "Bash", Pattern: "curl *"},
		{Tool: "Bash", Pattern: "wget *"},
	}

	if d := c.Check("Bash", bashParams("curl https://example.com"), allow, nil); d != Allowed {
		t.Errorf("first pattern: got %v, want Allowed", d)
	}
	if d := c.Check("Bash", bashParams("wget https://example.com"), allow, nil); d != Allowed {
		t.Errorf("second pattern: got %v, want Allowed", d)
	}
	if d := c.Check("Bash", bashParams("rm -rf /"), allow, nil); d != Denied {
		t.Errorf("no matching pattern: got %v, want Denied", d)
	}
}

func TestChecker_FilePathRules(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Read", Pattern: "/src/*"}}
	deny := []Rule{{Tool: "Write", Pattern: "/etc/*"}}

	if d := c.Check("Read", fileParams("/src/main.go"), allow, deny); d != Allowed {
		t.Errorf("allowed path: got %v, want Allowed", d)
	}
	if d := c.Check("Read", fileParams("/etc/passwd"), allow, deny); d != Denied {
		t.Errorf("disallowed path: got %v, want Denied", d)
	}
	if d := c.Check("Write", fileParams("/etc/shadow"), allow, deny); d != Denied {
		t.Errorf("denied write: got %v, want Denied", d)
	}
	if d := c.Check("Write", fileParams("/src/out.txt"), allow, deny); d != Unmatched {
		t.Errorf("unmatched write: got %v, want Unmatched", d)
	}
}

func TestChecker_SpawnInstanceRules(t *testing.T) {
	c := &Checker{}
	allow := []Rule{
		{Tool: "SpawnInstance", Pattern: "researcher"},
		{Tool: "SpawnInstance", Pattern: "coder"},
	}

	if d := c.Check("SpawnInstance", agentParams("researcher"), allow, nil); d != Allowed {
		t.Errorf("allowed agent: got %v, want Allowed", d)
	}
	if d := c.Check("SpawnInstance", agentParams("admin"), allow, nil); d != Denied {
		t.Errorf("disallowed agent: got %v, want Denied", d)
	}
}

func TestChecker_SpawnInstanceCommaList(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "SpawnInstance", Pattern: "worker,researcher"}}

	if d := c.Check("SpawnInstance", agentParams("worker"), allow, nil); d != Allowed {
		t.Errorf("worker in list: got %v, want Allowed", d)
	}
	if d := c.Check("SpawnInstance", agentParams("researcher"), allow, nil); d != Allowed {
		t.Errorf("researcher in list: got %v, want Allowed", d)
	}
	if d := c.Check("SpawnInstance", agentParams("admin"), allow, nil); d != Denied {
		t.Errorf("admin not in list: got %v, want Denied", d)
	}
}

func TestChecker_SpawnInstanceWildcardInList(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "SpawnInstance", Pattern: "research*,coder"}}

	if d := c.Check("SpawnInstance", agentParams("researcher"), allow, nil); d != Allowed {
		t.Errorf("wildcard in list: got %v, want Allowed", d)
	}
	if d := c.Check("SpawnInstance", agentParams("admin"), allow, nil); d != Denied {
		t.Errorf("not in list: got %v, want Denied", d)
	}
}

func TestChecker_AllowDenyDifferentPatterns(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash", Pattern: "curl *"}}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	if d := c.Check("Bash", bashParams("curl https://example.com"), allow, deny); d != Allowed {
		t.Errorf("allow match, deny miss: got %v, want Allowed", d)
	}
	if d := c.Check("Bash", bashParams("rm -rf /"), allow, deny); d != Denied {
		t.Errorf("deny match: got %v, want Denied", d)
	}
}

func TestChecker_WholeToolDenyOverridesParameterizedAllow(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash", Pattern: "curl *"}}
	deny := []Rule{{Tool: "Bash"}}

	if d := c.Check("Bash", bashParams("curl https://safe.com"), allow, deny); d != Denied {
		t.Errorf("whole-tool deny overrides: got %v, want Denied", d)
	}
}

func TestChecker_NoRules(t *testing.T) {
	c := &Checker{}
	if d := c.Check("Bash", bashParams("anything"), nil, nil); d != Unmatched {
		t.Errorf("no rules: got %v, want Unmatched", d)
	}
}

func TestChecker_EmptyAllowSlice(t *testing.T) {
	c := &Checker{}
	if d := c.Check("Bash", bashParams("anything"), []Rule{}, nil); d != Unmatched {
		t.Errorf("empty allow slice: got %v, want Unmatched", d)
	}
}

func TestChecker_UnknownTool(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "UnknownTool", Pattern: "foo"}}
	params := map[string]any{"x": "foo"}

	if d := c.Check("UnknownTool", params, allow, nil); d != Denied {
		t.Errorf("unknown tool parameterized: got %v, want Denied", d)
	}
	allow2 := []Rule{{Tool: "UnknownTool"}}
	if d := c.Check("UnknownTool", params, allow2, nil); d != Allowed {
		t.Errorf("unknown tool whole: got %v, want Allowed", d)
	}
}

func TestChecker_CustomExtractorsFallBackToDefaults(t *testing.T) {
	c := &Checker{
		Extractors: map[string]ParamExtractor{
			"MyTool": func(p map[string]any) string { s, _ := p["target"].(string); return s },
		},
	}
	allow := []Rule{{Tool: "Bash", Pattern: "curl *"}}
	if d := c.Check("Bash", bashParams("curl https://example.com"), allow, nil); d != Allowed {
		t.Errorf("builtin fallback: got %v, want Allowed", d)
	}
}

func TestChecker_NilParams(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash", Pattern: "curl *"}}
	if d := c.Check("Bash", nil, allow, nil); d != Denied {
		t.Errorf("nil params: got %v, want Denied", d)
	}
}

func TestChecker_PathTraversal(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Read", Pattern: "/src/*"}}

	// Path traversal is now blocked — filepath.Clean normalizes
	// /src/../etc/passwd to /etc/passwd, which doesn't match /src/*.
	if d := c.Check("Read", fileParams("/src/../etc/passwd"), allow, nil); d != Denied {
		t.Errorf("path traversal should be blocked: got %v, want Denied", d)
	}
	if d := c.Check("Read", fileParams("/etc/passwd"), allow, nil); d != Denied {
		t.Errorf("clean path outside prefix: got %v, want Denied", d)
	}
	// Clean path inside /src/ still allowed.
	if d := c.Check("Read", fileParams("/src/main.go"), allow, nil); d != Allowed {
		t.Errorf("clean path inside prefix: got %v, want Allowed", d)
	}
}

func TestDecisionString(t *testing.T) {
	tests := []struct {
		d    Decision
		want string
	}{
		{Allowed, "allowed"},
		{Denied, "denied"},
		{Unmatched, "unmatched"},
		{NeedsReview, "needs_review"},
		{Decision(99), "unmatched"},
	}
	for _, tt := range tests {
		if got := tt.d.String(); got != tt.want {
			t.Errorf("Decision(%d).String() = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// --- AST-based Bash matching (previously bypassed, now caught) ---

func TestChecker_BashCommandSubstitution(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	// rm inside $() is now extracted and matched by deny rule.
	if d := c.Check("Bash", bashParams("echo $(rm -rf /)"), nil, deny); d != Denied {
		t.Errorf("$() substitution: got %v, want Denied", d)
	}
	// Backtick form.
	if d := c.Check("Bash", bashParams("echo `rm -rf /`"), nil, deny); d != Denied {
		t.Errorf("backtick substitution: got %v, want Denied", d)
	}
}

func TestChecker_BashNestedSubstitution(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	if d := c.Check("Bash", bashParams("echo $(echo $(rm -rf /))"), nil, deny); d != Denied {
		t.Errorf("nested substitution: got %v, want Denied", d)
	}
}

func TestChecker_BashSubshell(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	if d := c.Check("Bash", bashParams("(rm -rf /)"), nil, deny); d != Denied {
		t.Errorf("subshell: got %v, want Denied", d)
	}
}

func TestChecker_BashCompoundDeny(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	if d := c.Check("Bash", bashParams("echo hello && rm -rf /"), nil, deny); d != Denied {
		t.Errorf("compound &&: got %v, want Denied", d)
	}
	if d := c.Check("Bash", bashParams("ls | rm -rf /"), nil, deny); d != Denied {
		t.Errorf("compound pipe: got %v, want Denied", d)
	}
	if d := c.Check("Bash", bashParams("echo test; rm -rf /"), nil, deny); d != Denied {
		t.Errorf("compound semicolon: got %v, want Denied", d)
	}
	if d := c.Check("Bash", bashParams("echo hello"), nil, deny); d != Unmatched {
		t.Errorf("no rm: got %v, want Unmatched", d)
	}
}

func TestChecker_BashEnvVarPrefix(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	// AST parser strips env var assignments, exposing the real command.
	if d := c.Check("Bash", bashParams("FOO=bar rm -rf /"), nil, deny); d != Denied {
		t.Errorf("env prefix: got %v, want Denied", d)
	}
}

func TestChecker_BashQuotedOperators(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	// && inside quotes is part of the argument, not an operator.
	if d := c.Check("Bash", bashParams(`echo "a && rm -rf /"`), nil, deny); d == Denied {
		t.Errorf("quoted &&: got Denied, want not Denied (rm is inside quotes)")
	}
}

// --- Dangerous builtins → NeedsReview ---

func TestChecker_BashDangerousBuiltins(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	// eval is a dangerous builtin. Even though "eval rm -rf /" contains
	// "rm -rf /" as an extracted command, eval itself marks as uncertain.
	// Since deny has uncertain result → NeedsReview.
	builtins := []string{
		`eval "rm -rf /"`,
		"exec rm -rf /",
		"source script.sh",
		". script.sh",
		"bash -c 'rm -rf /'",
	}
	for _, cmd := range builtins {
		d := c.Check("Bash", bashParams(cmd), nil, deny)
		if d != Denied && d != NeedsReview {
			t.Errorf("dangerous builtin %q: got %v, want Denied or NeedsReview", cmd, d)
		}
	}
}

func TestChecker_BashDangerousBuiltinNoMatchingDeny(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "curl *"}}

	// eval doesn't match "curl *", but eval is uncertain → NeedsReview.
	d := c.Check("Bash", bashParams(`eval "echo hello"`), nil, deny)
	if d != NeedsReview {
		t.Errorf("dangerous builtin with non-matching deny: got %v, want NeedsReview", d)
	}
}

func TestChecker_BashDangerousBuiltinWithAllowRule(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash", Pattern: "echo *"}}

	// eval is uncertain even for allow rules.
	d := c.Check("Bash", bashParams(`eval "echo hello"`), allow, nil)
	if d != NeedsReview {
		t.Errorf("dangerous builtin with allow: got %v, want NeedsReview", d)
	}
}

func TestChecker_BashParseError(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	d := c.Check("Bash", bashParams("if then fi (("), nil, deny)
	if d != NeedsReview {
		t.Errorf("parse error: got %v, want NeedsReview", d)
	}
}

// --- SpawnInstance adversarial ---

func TestChecker_SpawnInstanceTrailingComma(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "SpawnInstance", Pattern: "researcher,"}}

	if d := c.Check("SpawnInstance", agentParams(""), allow, nil); d != Denied {
		t.Errorf("trailing comma empty agent: got %v, want Denied", d)
	}
	if d := c.Check("SpawnInstance", agentParams("researcher"), allow, nil); d != Allowed {
		t.Errorf("trailing comma valid agent: got %v, want Allowed", d)
	}
}

func TestChecker_SpawnInstanceDoubleComma(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "SpawnInstance", Pattern: "worker,,coder"}}

	if d := c.Check("SpawnInstance", agentParams(""), allow, nil); d != Denied {
		t.Errorf("double comma empty agent: got %v, want Denied", d)
	}
	if d := c.Check("SpawnInstance", agentParams("worker"), allow, nil); d != Allowed {
		t.Errorf("double comma valid agent: got %v, want Allowed", d)
	}
}

// --- Wildcard adversarial ---

func TestAdversarial_WildcardDanglingBackslash(t *testing.T) {
	if MatchWildcard(`hello\`, "hello") {
		t.Error(`dangling \: should not match "hello"`)
	}
	if !MatchWildcard(`hello\`, `hello\`) {
		t.Error(`dangling \: should match "hello\"`)
	}
}

func TestAdversarial_WildcardConsecutiveEscapes(t *testing.T) {
	if !MatchWildcard(`\\\\`, `\\`) {
		t.Error(`\\\\ should match \\`)
	}
}

func TestAdversarial_WildcardEscapeWithStar(t *testing.T) {
	if !MatchWildcard(`\**`, "*anything") {
		t.Error(`\** should match "*anything"`)
	}
	if MatchWildcard(`\**`, "anything") {
		t.Error(`\** should not match "anything" (no leading *)`)
	}
}

// Dangerous builtin with no rules at all → Unmatched, not NeedsReview.
func TestChecker_DangerousBuiltinNoRules(t *testing.T) {
	c := &Checker{}
	d := c.Check("Bash", bashParams(`eval "anything"`), nil, nil)
	if d != Unmatched {
		t.Errorf("no rules + dangerous builtin: got %v, want Unmatched", d)
	}
}

// Whole-tool allow with dangerous builtin. When the pattern definitively
// matches the extracted command, the match wins over uncertainty.
// If an operator writes Bash(*), they've explicitly allowed everything.
func TestChecker_BashWildcardAllowWithDangerousBuiltin(t *testing.T) {
	c := &Checker{}
	allow := []Rule{{Tool: "Bash", Pattern: "*"}}

	// Bash(*) matches everything, including eval commands.
	d := c.Check("Bash", bashParams(`eval "echo hello"`), allow, nil)
	if d != Allowed {
		t.Errorf("wildcard allow + eval: got %v, want Allowed", d)
	}

	// Simple command with wildcard allow → Allowed.
	d = c.Check("Bash", bashParams("echo hello"), allow, nil)
	if d != Allowed {
		t.Errorf("wildcard allow + simple cmd: got %v, want Allowed", d)
	}

	// But a narrow allow pattern + dangerous builtin → NeedsReview,
	// because the narrow pattern can't guarantee what eval runs.
	allow2 := []Rule{{Tool: "Bash", Pattern: "echo *"}}
	d = c.Check("Bash", bashParams(`eval "echo hello"`), allow2, nil)
	if d != NeedsReview {
		t.Errorf("narrow allow + eval: got %v, want NeedsReview", d)
	}
}

// --- Security fix verification ---

// ANSI-C quoting: $'\x72\x6d' expands to "rm" at runtime.
func TestChecker_BashAnsiCQuoting(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	// $'...' is opaque — cannot decode statically → NeedsReview.
	d := c.Check("Bash", bashParams(`$'\x72\x6d' -rf /`), nil, deny)
	if d != NeedsReview {
		t.Errorf("ANSI-C quoting: got %v, want NeedsReview", d)
	}
}

// Command wrappers: sudo, command, builtin, nohup, nice.
func TestChecker_BashCommandWrappers(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	wrappers := []string{
		"sudo rm -rf /",
		"command rm -rf /",
		"builtin rm -rf /",
		"nohup rm -rf /",
		"nice rm -rf /",
	}
	for _, cmd := range wrappers {
		d := c.Check("Bash", bashParams(cmd), nil, deny)
		if d != NeedsReview {
			t.Errorf("wrapper %q: got %v, want NeedsReview", cmd, d)
		}
	}
}

// Variable expansion in command position.
func TestChecker_BashVariableAsCommand(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	// $CMD is unknowable at analysis time → NeedsReview.
	d := c.Check("Bash", bashParams("$CMD -rf /"), nil, deny)
	if d != NeedsReview {
		t.Errorf("variable as command: got %v, want NeedsReview", d)
	}
}

// Script interpreters.
func TestChecker_BashScriptInterpreters(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	interpreters := []string{
		`python3 -c 'import os; os.system("rm -rf /")'`,
		`perl -e 'system("rm -rf /")'`,
		`node -e 'require("child_process").execSync("rm -rf /")'`,
		`ruby -e 'system("rm -rf /")'`,
	}
	for _, cmd := range interpreters {
		d := c.Check("Bash", bashParams(cmd), nil, deny)
		if d != NeedsReview {
			t.Errorf("interpreter %q: got %v, want NeedsReview", cmd, d)
		}
	}
}

// trap builtin.
func TestChecker_BashTrap(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Bash", Pattern: "rm *"}}

	d := c.Check("Bash", bashParams("trap 'rm -rf /' EXIT"), nil, deny)
	if d != NeedsReview && d != Denied {
		t.Errorf("trap: got %v, want NeedsReview or Denied", d)
	}
}

// Path traversal now blocked by filepath.Clean in extractors.
func TestChecker_PathTraversalDeny(t *testing.T) {
	c := &Checker{}
	deny := []Rule{{Tool: "Read", Pattern: "/etc/*"}}

	// Direct path is caught.
	if d := c.Check("Read", fileParams("/etc/passwd"), nil, deny); d != Denied {
		t.Errorf("direct path: got %v, want Denied", d)
	}
	// Traversal is normalized to /etc/passwd → caught.
	if d := c.Check("Read", fileParams("/src/../etc/passwd"), nil, deny); d != Denied {
		t.Errorf("traversal path: got %v, want Denied", d)
	}
}

// --- Helpers ---

func sliceContainsAll(haystack, needles []string) bool {
	set := make(map[string]bool, len(haystack))
	for _, s := range haystack {
		set[s] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}
