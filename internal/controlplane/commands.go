package controlplane

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nchapman/hiro/internal/toolrules"
)

// minSubcommandArgs is the minimum number of arguments for commands that
// require a verb plus at least one argument (e.g. "/tools set <agent> <tools>").
const minSubcommandArgs = 2

// HandleCommand parses and executes a slash command. Returns a
// human-readable result string. Returns an error if the command is
// unrecognized or the sub-command is invalid.
func (cp *ControlPlane) HandleCommand(input string) (string, error) {
	input = strings.TrimPrefix(input, "/")
	parts := strings.Fields(input)
	if len(parts) < 1 {
		return "", fmt.Errorf("empty command")
	}

	noun := parts[0]
	verb := ""
	var args []string
	if len(parts) > 1 {
		verb = parts[1]
	}
	if len(parts) > minSubcommandArgs {
		args = parts[2:]
	}

	var result string
	var mutated bool
	var err error
	switch noun {
	case "help":
		return cp.handleHelp()
	case "secrets":
		result, mutated, err = cp.handleSecrets(verb, args)
	case "tools":
		result, mutated, err = cp.handleTools(verb, args)
	case "cluster":
		result, err = cp.handleCluster(verb)
	default:
		return "", fmt.Errorf("unknown command: %s", noun)
	}
	if err != nil {
		return result, err
	}

	// Log and save to disk after any mutation so config.yaml stays in sync.
	if mutated {
		cp.logger.Info("config changed via command", "command", noun, "verb", verb)
		if saveErr := cp.Save(); saveErr != nil {
			cp.logger.Warn("failed to save config after command", "error", saveErr)
			// Intentionally use a static message — do not include saveErr, which
			// contains the config file path and could leak it to the user.
			result += "\n\nWarning: failed to save to disk. Change is active but may not survive a restart."
		}
	}

	return result, nil
}

func (cp *ControlPlane) handleSecrets(verb string, args []string) (string, bool, error) {
	switch verb {
	case "set":
		if len(args) < 1 {
			return "Usage: /secrets set NAME=VALUE", false, nil
		}
		// Support both "NAME=VALUE" and "NAME VALUE" forms.
		name, value, ok := parseKeyValue(args)
		if !ok {
			return "Usage: /secrets set NAME=VALUE", false, nil
		}
		cp.SetSecret(name, value)
		return fmt.Sprintf("Secret %q set.", name), true, nil

	case "rm", "remove", "delete":
		if len(args) < 1 {
			return "Usage: /secrets rm NAME", false, nil
		}
		name := args[0]
		cp.DeleteSecret(name)
		return fmt.Sprintf("Secret %q removed.", name), true, nil

	case "list", "ls":
		names := cp.SecretNames()
		if len(names) == 0 {
			return "No secrets configured.", false, nil
		}
		var b strings.Builder
		b.WriteString("Secrets:\n")
		for _, name := range names {
			fmt.Fprintf(&b, "  • %s\n", name)
		}
		return strings.TrimRight(b.String(), "\n"), false, nil

	case "":
		return "Usage: /secrets <set|rm|list>", false, nil

	default:
		return "", false, fmt.Errorf("unknown secrets command: %s", verb)
	}
}

func (cp *ControlPlane) handleTools(verb string, args []string) (string, bool, error) {
	switch verb {
	case "set":
		return cp.handleToolsSetOrDeny(args, "allow", "Usage: /tools set <agent> <tool1,tool2,...>", cp.SetAgentTools)

	case "deny":
		return cp.handleToolsSetOrDeny(args, "deny", "Usage: /tools deny <agent> <rule1,rule2,...>", cp.SetAgentDisallowedTools)

	case "rm", "remove", "clear":
		if len(args) < 1 {
			return "Usage: /tools rm <agent>", false, nil
		}
		agentName := args[0]
		cp.ClearAgentTools(agentName)
		cp.ClearAgentDisallowedTools(agentName)
		return fmt.Sprintf("Tool overrides for %q cleared. Agent will use its declared tools.", agentName), true, nil

	case "list", "ls":
		return cp.handleToolsList(args), false, nil

	case "":
		return "Usage: /tools <set|deny|rm|list>", false, nil

	default:
		return "", false, fmt.Errorf("unknown tools command: %s", verb)
	}
}

// handleToolsSetOrDeny validates and applies an allow or deny rule list for an agent.
func (cp *ControlPlane) handleToolsSetOrDeny(args []string, label, usage string, apply func(string, []string)) (string, bool, error) {
	if len(args) < minSubcommandArgs {
		return usage, false, nil
	}
	agentName := args[0]
	toolList := parseToolList(args[1:])
	if len(toolList) == 0 {
		return usage, false, nil
	}
	if _, err := toolrules.ParseRules(toolList); err != nil {
		return fmt.Sprintf("Invalid rule: %v", err), false, nil
	}
	apply(agentName, toolList)
	capLabel := strings.ToUpper(label[:1]) + label[1:]
	return fmt.Sprintf("%s rules for %q set to: %s\n\nTakes effect on the agent's next turn.",
		capLabel, agentName, strings.Join(toolList, ", ")), true, nil
}

// handleToolsList formats tool policy overrides for display.
func (cp *ControlPlane) handleToolsList(args []string) string {
	if len(args) > 0 {
		return cp.formatToolPolicy(args[0])
	}
	policies := cp.AllPolicies()
	if len(policies) == 0 {
		return "No tool overrides configured. All agents use their declared tools."
	}
	// Sort agent names for consistent output.
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("Tool overrides:\n")
	for _, name := range names {
		p := policies[name]
		fmt.Fprintf(&b, "  %s:\n", name)
		if len(p.AllowedTools) > 0 {
			fmt.Fprintf(&b, "    allow: %s\n", strings.Join(p.AllowedTools, ", "))
		}
		if len(p.DisallowedTools) > 0 {
			fmt.Fprintf(&b, "    deny:  %s\n", strings.Join(p.DisallowedTools, ", "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatToolPolicy returns a human-readable summary of an agent's tool policy.
func (cp *ControlPlane) formatToolPolicy(agentName string) string {
	tools, hasTools := cp.AgentTools(agentName)
	denyTools := cp.AgentDisallowedTools(agentName)
	if !hasTools && len(denyTools) == 0 {
		return fmt.Sprintf("No tool override for %q. Agent uses its declared tools.", agentName)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tool overrides for %q:", agentName)
	if hasTools {
		fmt.Fprintf(&b, "\n  allow: %s", strings.Join(tools, ", "))
	}
	if len(denyTools) > 0 {
		fmt.Fprintf(&b, "\n  deny:  %s", strings.Join(denyTools, ", "))
	}
	return b.String()
}

// parseKeyValue parses "NAME=VALUE" or "NAME" "VALUE" from args.
func parseKeyValue(args []string) (name, value string, ok bool) {
	if len(args) == 0 {
		return "", "", false
	}
	// "NAME=VALUE" form
	if i := strings.IndexByte(args[0], '='); i > 0 {
		name = args[0][:i]
		value = args[0][i+1:]
		// Allow value to contain spaces: join remaining args.
		if len(args) > 1 {
			value = value + " " + strings.Join(args[1:], " ")
		}
		return name, value, true
	}
	// "NAME VALUE" form
	if len(args) >= minSubcommandArgs {
		return args[0], strings.Join(args[1:], " "), true
	}
	return "", "", false
}

func (cp *ControlPlane) handleCluster(verb string) (string, error) {
	switch verb {
	case "":
		mode := cp.ClusterMode()
		if mode == "" {
			return "Cluster not configured. Use the web UI to set up your deployment mode.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Mode: %s\n", mode)
		if code := cp.ClusterSwarmCode(); code != "" {
			fmt.Fprintf(&b, "Swarm code: %s\n", code)
		}
		if name := cp.ClusterNodeName(); name != "" {
			fmt.Fprintf(&b, "Node name: %s\n", name)
		}
		if approved := cp.ApprovedNodes(); len(approved) > 0 {
			fmt.Fprintf(&b, "Approved nodes: %d\n", len(approved))
		}
		return strings.TrimRight(b.String(), "\n"), nil

	default:
		return "", fmt.Errorf("unknown cluster command: %s", verb)
	}
}

// handleHelp returns a list of all slash commands. Note: /clear is handled by
// the WebSocket layer (chat.go), not by HandleCommand. Update this text if
// commands are added to either location.
func (cp *ControlPlane) handleHelp() (string, error) {
	return `Available commands:

/help                              Show this help
/clear                             Start a new session
/secrets list                      List secret names
/secrets set NAME=VALUE            Set a secret
/secrets rm NAME                   Remove a secret
/tools list [AGENT]                List tool overrides
/tools set AGENT rule1,rule2       Set allow rules (e.g. Bash(curl *),Read)
/tools deny AGENT rule1,rule2      Set deny rules (e.g. Bash(rm *))
/tools rm AGENT                    Clear all tool overrides
/cluster                           Show cluster status`, nil
}

// parseToolList parses tool rules from args. Rules can be comma-separated
// or space-separated. Commas inside parentheses are preserved (e.g.,
// "SpawnInstance(worker,researcher)" stays as one rule).
func parseToolList(args []string) []string {
	// Rejoin into a single string so that space-separated args recombine
	// parameterized rules like "Bash(curl" and "*)".
	raw := strings.Join(args, " ")

	var result []string
	depth := 0
	start := 0
	for i := range len(raw) {
		switch raw[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if t := strings.TrimSpace(raw[start:i]); t != "" {
					result = append(result, t)
				}
				start = i + 1
			}
		}
	}
	if t := strings.TrimSpace(raw[start:]); t != "" {
		result = append(result, t)
	}
	return result
}
