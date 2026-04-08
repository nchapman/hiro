package controlplane

import (
	"fmt"
	"strings"
)

// minSubcommandArgs is the minimum number of arguments for commands that
// require a verb plus at least one argument (e.g. "/secrets set NAME=VALUE").
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
		if setErr := cp.SetSecret(name, value); setErr != nil {
			return setErr.Error(), false, nil //nolint:nilerr // error reported as user-facing message
		}
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

// handleHelp returns a list of all slash commands. /clear is handled by
// the Router (router.go), not by HandleCommand. Update this text if
// commands are added to either location.
func (cp *ControlPlane) handleHelp() (string, error) {
	return `Available commands:

/help                    Show this help
/clear                   Start a new conversation
/secrets list            List secret names (web only)
/secrets set NAME=VALUE  Set a secret (web only)
/secrets rm NAME         Remove a secret (web only)
/cluster                 Show cluster status (web only)`, nil
}
