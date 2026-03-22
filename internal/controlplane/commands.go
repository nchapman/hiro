package controlplane

import (
	"fmt"
	"sort"
	"strings"
)

// HandleCommand parses and executes a slash command. Returns a
// human-readable result string. If the input is not a recognized
// command, returns an error — the caller should fall through to the
// agent rather than displaying an error to the user.
func (cp *ControlPlane) HandleCommand(input string) (string, error) {
	input = strings.TrimPrefix(input, "/")
	parts := strings.Fields(input)
	if len(parts) < 1 {
		return "", fmt.Errorf("empty command")
	}

	noun := parts[0]
	verb := ""
	if len(parts) > 1 {
		verb = parts[1]
	}
	args := parts[2:]

	switch noun {
	case "secrets":
		return cp.handleSecrets(verb, args)
	case "tools":
		return cp.handleTools(verb, args)
	default:
		return "", fmt.Errorf("unknown command: %s", noun)
	}
}

func (cp *ControlPlane) handleSecrets(verb string, args []string) (string, error) {
	switch verb {
	case "set":
		if len(args) < 1 {
			return "Usage: /secrets set NAME=VALUE", nil
		}
		// Support both "NAME=VALUE" and "NAME VALUE" forms.
		name, value, ok := parseKeyValue(args)
		if !ok {
			return "Usage: /secrets set NAME=VALUE", nil
		}
		cp.SetSecret(name, value)
		return fmt.Sprintf("Secret %q set.", name), nil

	case "rm", "remove", "delete":
		if len(args) < 1 {
			return "Usage: /secrets rm NAME", nil
		}
		name := args[0]
		cp.DeleteSecret(name)
		return fmt.Sprintf("Secret %q removed.", name), nil

	case "list", "ls":
		names := cp.SecretNames()
		if len(names) == 0 {
			return "No secrets configured.", nil
		}
		var b strings.Builder
		b.WriteString("Secrets:\n")
		for _, name := range names {
			fmt.Fprintf(&b, "  • %s\n", name)
		}
		return strings.TrimRight(b.String(), "\n"), nil

	case "":
		return "Usage: /secrets <set|rm|list>", nil

	default:
		return "", fmt.Errorf("unknown secrets command: %s", verb)
	}
}

func (cp *ControlPlane) handleTools(verb string, args []string) (string, error) {
	switch verb {
	case "set":
		if len(args) < 2 {
			return "Usage: /tools set <agent> <tool1,tool2,...>", nil
		}
		agentName := args[0]
		toolList := parseToolList(args[1:])
		if len(toolList) == 0 {
			return "Usage: /tools set <agent> <tool1,tool2,...>", nil
		}
		cp.SetAgentTools(agentName, toolList)
		return fmt.Sprintf("Tools for %q set to: %s\n\nNote: takes effect on next agent start, not for running agents.", agentName, strings.Join(toolList, ", ")), nil

	case "rm", "remove", "clear":
		if len(args) < 1 {
			return "Usage: /tools rm <agent>", nil
		}
		agentName := args[0]
		cp.ClearAgentTools(agentName)
		return fmt.Sprintf("Tool override for %q cleared. Agent will use its declared tools.", agentName), nil

	case "list", "ls":
		if len(args) > 0 {
			agentName := args[0]
			tools, ok := cp.AgentTools(agentName)
			if !ok {
				return fmt.Sprintf("No tool override for %q. Agent uses its declared tools.", agentName), nil
			}
			return fmt.Sprintf("Tool override for %q: %s", agentName, strings.Join(tools, ", ")), nil
		}
		policies := cp.AllPolicies()
		if len(policies) == 0 {
			return "No tool overrides configured. All agents use their declared tools.", nil
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
			fmt.Fprintf(&b, "  %s: %s\n", name, strings.Join(policies[name].Tools, ", "))
		}
		return strings.TrimRight(b.String(), "\n"), nil

	case "":
		return "Usage: /tools <set|rm|list>", nil

	default:
		return "", fmt.Errorf("unknown tools command: %s", verb)
	}
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
	if len(args) >= 2 {
		return args[0], strings.Join(args[1:], " "), true
	}
	return "", "", false
}

// parseToolList parses tool names from args. Tools can be comma-separated
// in a single arg or space-separated across args.
func parseToolList(args []string) []string {
	var tools []string
	for _, arg := range args {
		for _, t := range strings.Split(arg, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tools = append(tools, t)
			}
		}
	}
	return tools
}
