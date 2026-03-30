package controlplane

import (
	"crypto/rand"
	"encoding/hex"
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

	var result string
	var mutated bool
	var err error
	switch noun {
	case "secrets":
		result, mutated, err = cp.handleSecrets(verb, args)
	case "tools":
		result, mutated, err = cp.handleTools(verb, args)
	case "cluster":
		result, mutated, err = cp.handleCluster(verb, args)
	default:
		return "", fmt.Errorf("unknown command: %s", noun)
	}
	if err != nil {
		return result, err
	}

	// Save to disk after any mutation so config.yaml stays in sync.
	if mutated {
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
		if len(args) < 2 {
			return "Usage: /tools set <agent> <tool1,tool2,...>", false, nil
		}
		agentName := args[0]
		toolList := parseToolList(args[1:])
		if len(toolList) == 0 {
			return "Usage: /tools set <agent> <tool1,tool2,...>", false, nil
		}
		cp.SetAgentTools(agentName, toolList)
		return fmt.Sprintf("Tools for %q set to: %s\n\nNote: takes effect on next agent start, not for running agents.", agentName, strings.Join(toolList, ", ")), true, nil

	case "rm", "remove", "clear":
		if len(args) < 1 {
			return "Usage: /tools rm <agent>", false, nil
		}
		agentName := args[0]
		cp.ClearAgentTools(agentName)
		return fmt.Sprintf("Tool override for %q cleared. Agent will use its declared tools.", agentName), true, nil

	case "list", "ls":
		if len(args) > 0 {
			agentName := args[0]
			tools, ok := cp.AgentTools(agentName)
			if !ok {
				return fmt.Sprintf("No tool override for %q. Agent uses its declared tools.", agentName), false, nil
			}
			return fmt.Sprintf("Tool override for %q: %s", agentName, strings.Join(tools, ", ")), false, nil
		}
		policies := cp.AllPolicies()
		if len(policies) == 0 {
			return "No tool overrides configured. All agents use their declared tools.", false, nil
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
		return strings.TrimRight(b.String(), "\n"), false, nil

	case "":
		return "Usage: /tools <set|rm|list>", false, nil

	default:
		return "", false, fmt.Errorf("unknown tools command: %s", verb)
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

func (cp *ControlPlane) handleCluster(verb string, args []string) (string, bool, error) {
	switch verb {
	case "token":
		if len(args) < 1 {
			return "Usage: /cluster token <create|revoke|list>", false, nil
		}
		return cp.handleClusterToken(args[0], args[1:])

	case "":
		return "Usage: /cluster token <create|revoke|list>", false, nil

	default:
		return "", false, fmt.Errorf("unknown cluster command: %s", verb)
	}
}

func (cp *ControlPlane) handleClusterToken(action string, args []string) (string, bool, error) {
	switch action {
	case "create":
		name := "default"
		if len(args) > 0 {
			name = args[0]
		}
		// Generate a random 32-byte token.
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", false, fmt.Errorf("generating token: %w", err)
		}
		token := hex.EncodeToString(b)
		cp.SetClusterJoinToken(name, token)
		return fmt.Sprintf("Join token %q created:\n\n  %s\n\nWorkers use this to connect. Store it securely.", name, token), true, nil

	case "revoke", "rm", "delete":
		if len(args) < 1 {
			return "Usage: /cluster token revoke <name>", false, nil
		}
		name := args[0]
		cp.DeleteClusterJoinToken(name)
		return fmt.Sprintf("Join token %q revoked.", name), true, nil

	case "list", "ls":
		tokens := cp.ClusterJoinTokens()
		if len(tokens) == 0 {
			return "No join tokens configured. Use `/cluster token create [name]` to create one.", false, nil
		}
		names := make([]string, 0, len(tokens))
		for name := range tokens {
			names = append(names, name)
		}
		sort.Strings(names)

		var b strings.Builder
		b.WriteString("Join tokens:\n")
		for _, name := range names {
			// Show only first/last 4 chars of token for security.
			token := tokens[name]
			masked := token[:4] + "..." + token[len(token)-4:]
			fmt.Fprintf(&b, "  • %s: %s\n", name, masked)
		}
		return strings.TrimRight(b.String(), "\n"), false, nil

	default:
		return "", false, fmt.Errorf("unknown cluster token command: %s", action)
	}
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
