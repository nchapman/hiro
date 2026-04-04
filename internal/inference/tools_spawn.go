package inference

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/ipc"
)

//go:embed spawn_instance.md
var spawnInstanceDescription string

//go:embed create_persistent_instance.md
var createPersistentInstanceDescription string

//go:embed list_nodes.md
var listNodesDescription string

//go:embed resume_instance.md
var resumeInstanceDescription string

//go:embed list_instances.md
var listInstancesDescription string

//go:embed send_message.md
var sendMessageDescription string

//go:embed stop_instance.md
var stopInstanceDescription string

//go:embed delete_instance.md
var deleteInstanceDescription string

// ScopedManager wraps an ipc.HostManager with a caller ID, enforcing
// descendant scoping on all instance management operations.
type ScopedManager struct {
	mgr      ipc.HostManager
	callerID string
}

// NewScopedManager creates a scoped manager for the given caller instance.
func NewScopedManager(mgr ipc.HostManager, callerID string) *ScopedManager {
	return &ScopedManager{mgr: mgr, callerID: callerID}
}

func (s *ScopedManager) checkDescendant(targetID string) error {
	if !s.mgr.IsDescendant(targetID, s.callerID) {
		return fmt.Errorf("instance %q is not a descendant of caller %q", targetID, s.callerID)
	}
	return nil
}

// Tool name constants used for registration and provider filtering.
const (
	SpawnToolName            = "SpawnInstance"
	CreatePersistentToolName = "CreatePersistentInstance"
)

// --- Spawn tool (ephemeral) ---

// AgentListingProvider returns a ContextProvider that announces available
// agents as a <system-reminder> delta message. Only emits when the agent set
// changes compared to what was previously announced in the conversation.
func AgentListingProvider(mgr ipc.HostManager) ContextProvider {
	return func(activeTools map[string]bool, history []fantasy.Message) *DeltaResult {
		if !activeTools[SpawnToolName] && !activeTools[CreatePersistentToolName] {
			return nil
		}

		// Reconstruct announced set from prior deltas.
		announced := replayAnnounced("agents", history)

		// Get current state.
		defs := mgr.ListAgentDefs()
		current := make(map[string]bool, len(defs))
		for _, d := range defs {
			current[d.Name] = true
		}

		// Compute diff.
		var added, removed []string
		for name := range current {
			if !announced[name] {
				added = append(added, name)
			}
		}
		for name := range announced {
			if !current[name] {
				removed = append(removed, name)
			}
		}

		// Nothing changed → no new message.
		if len(added) == 0 && len(removed) == 0 {
			return nil
		}

		// Render the message text.
		isInitial := len(announced) == 0
		text := renderAgentListing(defs, added, removed, isInitial)

		return &DeltaResult{
			Message: buildDeltaMessage(text, "agents", added, removed),
		}
	}
}

// renderAgentListing produces the human-readable text for the agent listing delta.
func renderAgentListing(defs []ipc.AgentDef, added, removed []string, isInitial bool) string {
	var b strings.Builder
	if isInitial {
		b.WriteString("Available agents (descriptions tell you when to use each and what context to provide):\n\n")
		for _, d := range defs {
			fmt.Fprintf(&b, "- **%s**: %s\n", d.Name, d.Description)
		}
		return b.String()
	}
	if len(added) > 0 {
		b.WriteString("New agents available:\n\n")
		addedSet := make(map[string]bool, len(added))
		for _, name := range added {
			addedSet[name] = true
		}
		for _, d := range defs {
			if addedSet[d.Name] {
				fmt.Fprintf(&b, "- **%s**: %s\n", d.Name, d.Description)
			}
		}
	}
	if len(removed) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		sorted := make([]string, len(removed))
		copy(sorted, removed)
		sort.Strings(sorted)
		b.WriteString("Agents no longer available:\n")
		for _, name := range sorted {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	return b.String()
}

func buildSpawnTool(mgr ipc.HostManager, notifications *NotificationQueue, sessionID string, logger *slog.Logger) Tool {
	return Tool{
		AgentTool: fantasy.NewAgentTool(SpawnToolName,
			spawnInstanceDescription,
			func(ctx context.Context, input struct {
				Agent      string `json:"agent"      description:"Agent definition name (directory under agents/)."`
				Prompt     string `json:"prompt"     description:"The task for the agent to complete."`
				Background bool   `json:"background" description:"Run in the background. Returns immediately; you'll be notified when it completes."`
				Node       string `json:"node"       description:"Target node name. Omit for local."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.Agent == "" {
					return fantasy.NewTextErrorResponse("agent name is required"), nil
				}
				if input.Prompt == "" {
					return fantasy.NewTextErrorResponse("prompt is required"), nil
				}

				callerID := callerIDFromContext(ctx)
				logger.Info("tool call", "tool", "SpawnInstance", "agent", input.Agent, "background", input.Background)

				if input.Background {
					launchBackgroundSpawn(context.WithoutCancel(ctx), mgr, notifications, sessionID, logger,
						input.Agent, input.Prompt, callerID, input.Node)
					return fantasy.NewTextResponse(
						fmt.Sprintf("Agent %q launched in background. You'll be notified when it completes.", input.Agent)), nil
				}

				result, err := mgr.SpawnEphemeral(ctx, input.Agent, input.Prompt, callerID, input.Node, nil)
				if err != nil {
					logger.Warn("SpawnInstance failed", "agent", input.Agent, "error", err)
					return fantasy.NewTextErrorResponse(fmt.Sprintf("instance failed: %v", err)), nil
				}
				return fantasy.NewTextResponse(truncateResult(result)), nil
			},
		),
	}
}

func launchBackgroundSpawn(ctx context.Context, mgr ipc.HostManager, notifications *NotificationQueue, sessionID string, logger *slog.Logger, agent, prompt, callerID, nodeID string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("background SpawnInstance panicked", "agent", agent, "panic", r)
				notifications.Push(Notification{
					Content:   fmt.Sprintf("<agent-notification>\n<agent>%s</agent>\n<status>failed</status>\n<summary>Agent %q crashed unexpectedly</summary>\n<result></result>\n</agent-notification>", agent, agent),
					Source:    "agent-completion",
					SessionID: sessionID,
				})
			}
		}()
		result, err := mgr.SpawnEphemeral(ctx, agent, prompt, callerID, nodeID, nil)
		status := "completed"
		summary := fmt.Sprintf("Agent %q finished", agent)
		if err != nil {
			status = "failed"
			summary = fmt.Sprintf("Agent %q failed: %v", agent, err)
			result = ""
			logger.Warn("background SpawnInstance failed", "agent", agent, "error", err)
		}
		content := fmt.Sprintf(
			"<agent-notification>\n<agent>%s</agent>\n<status>%s</status>\n<summary>%s</summary>\n<result>%s</result>\n</agent-notification>",
			agent, status, summary, truncateResult(result))
		notifications.Push(Notification{
			Content:   content,
			Source:    "agent-completion",
			SessionID: sessionID,
		})
	}()
}

// --- CreatePersistentInstance tool ---

func buildCreatePersistentInstanceTool(mgr ipc.HostManager, logger *slog.Logger) Tool {
	return Tool{
		AgentTool: fantasy.NewAgentTool(CreatePersistentToolName,
			createPersistentInstanceDescription,
			func(ctx context.Context, input struct {
				Agent       string `json:"agent"       description:"Agent definition name (directory under agents/)."`
				Name        string `json:"name"        description:"Display name for this instance. Defaults to the agent definition name."`
				Description string `json:"description" description:"Display description for this instance. Defaults to the agent definition description."`
				Persona     string `json:"persona"     description:"Initial persona instructions for this instance. Specializes the agent for a specific role, project, or personality. Injected into the system prompt under ## Persona."`
				Node        string `json:"node"        description:"Target node name. Omit for local."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.Agent == "" {
					return fantasy.NewTextErrorResponse("agent name is required"), nil
				}

				const modePersistent = "persistent"
				mode := modePersistent

				callerID := callerIDFromContext(ctx)
				nodeID := input.Node

				logger.Info("tool call", "tool", "CreatePersistentInstance", "agent", input.Agent, "mode", mode, "name", input.Name)

				id, err := mgr.CreateInstance(ctx, input.Agent, callerID, mode, nodeID, input.Name, input.Description, input.Persona)
				if err != nil {
					logger.Warn("CreatePersistentInstance failed", "agent", input.Agent, "mode", mode, "error", err)
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to create instance: %v", err)), nil
				}
				displayLabel := input.Name
				if displayLabel == "" {
					displayLabel = input.Agent
				}
				return fantasy.NewTextResponse(
					fmt.Sprintf("Instance %q created from %q with ID: %s (mode: %s). Use SendMessage to communicate with it.", displayLabel, input.Agent, id, mode)), nil
			},
		),
	}
}

// --- Management tools ---

func buildCoordinatorTools(mgr ipc.HostManager, logger *slog.Logger) []Tool {
	return wrapAll([]fantasy.AgentTool{
		buildResumeInstance(mgr, logger),
		buildListInstances(mgr, logger),
		buildListNodes(mgr, logger),
		buildSendMessage(mgr, logger),
		buildStopInstance(mgr, logger),
		buildDeleteInstance(mgr, logger),
	})
}

func buildListNodes(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("ListNodes",
		listNodesDescription,
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			logger.Debug("tool call", "tool", "ListNodes")
			nodes := mgr.ListNodes()
			if len(nodes) == 0 {
				return fantasy.NewTextResponse("No cluster nodes configured. All agents run locally."), nil
			}
			var sb strings.Builder
			for _, n := range nodes {
				label := n.Name
				if n.IsHome {
					label += " (home)"
				}
				fmt.Fprintf(&sb, "- **%s** (id: %s, status: %s", label, n.ID, n.Status)
				if n.Capacity > 0 {
					fmt.Fprintf(&sb, ", capacity: %d", n.Capacity)
				}
				fmt.Fprintf(&sb, ", active: %d)\n", n.ActiveCount)
			}
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}

type resumeInstanceInput struct {
	InstanceID string `json:"instance_id" description:"ID of a stopped instance."`
}

func buildResumeInstance(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("ResumeInstance",
		resumeInstanceDescription,
		func(ctx context.Context, input resumeInstanceInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.InstanceID == "" {
				return fantasy.NewTextErrorResponse("instance_id is required"), nil
			}
			callerID := callerIDFromContext(ctx)
			scoped := NewScopedManager(mgr, callerID)
			if err := scoped.checkDescendant(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			logger.Info("tool call", "tool", "ResumeInstance", "target_instance", input.InstanceID)
			if err := mgr.StartInstance(ctx, input.InstanceID); err != nil {
				logger.Warn("ResumeInstance failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to resume instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s resumed.", input.InstanceID)), nil
		},
	)
}

func buildListInstances(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("ListInstances",
		listInstancesDescription,
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			logger.Debug("tool call", "tool", "ListInstances")
			callerID := callerIDFromContext(ctx)
			instances := mgr.ListChildInstances(callerID)
			if len(instances) == 0 {
				return fantasy.NewTextResponse("No child instances."), nil
			}
			var sb strings.Builder
			for _, inst := range instances {
				fmt.Fprintf(&sb, "- **%s** (id: %s, mode: %s, status: %s)", inst.Name, inst.ID, inst.Mode, inst.Status)
				if inst.Description != "" {
					fmt.Fprintf(&sb, ": %s", inst.Description)
				}
				sb.WriteString("\n")
			}
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}

type sendMessageInput struct {
	InstanceID string `json:"instance_id" description:"Child instance ID."`
	Message    string `json:"message"     description:"The message to send."`
}

func buildSendMessage(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("SendMessage",
		sendMessageDescription,
		func(ctx context.Context, input sendMessageInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.InstanceID == "" {
				return fantasy.NewTextErrorResponse("instance_id is required"), nil
			}
			if input.Message == "" {
				return fantasy.NewTextErrorResponse("message is required"), nil
			}
			callerID := callerIDFromContext(ctx)
			scoped := NewScopedManager(mgr, callerID)
			if err := scoped.checkDescendant(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			logger.Info("tool call", "tool", "SendMessage", "target_instance", input.InstanceID)
			result, err := mgr.SendMessage(ctx, input.InstanceID, input.Message, nil)
			if err != nil {
				logger.Warn("SendMessage failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("SendMessage failed: %v", err)), nil
			}
			return fantasy.NewTextResponse(truncateResult(result)), nil
		},
	)
}

type stopInstanceInput struct {
	InstanceID string `json:"instance_id" description:"Child instance ID."`
}

func buildStopInstance(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("StopInstance",
		stopInstanceDescription,
		func(ctx context.Context, input stopInstanceInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.InstanceID == "" {
				return fantasy.NewTextErrorResponse("instance_id is required"), nil
			}
			callerID := callerIDFromContext(ctx)
			scoped := NewScopedManager(mgr, callerID)
			if err := scoped.checkDescendant(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			logger.Info("tool call", "tool", "StopInstance", "target_instance", input.InstanceID)
			if _, err := mgr.StopInstance(input.InstanceID); err != nil {
				logger.Warn("StopInstance failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to stop instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s stopped.", input.InstanceID)), nil
		},
	)
}

type deleteInstanceInput struct {
	InstanceID string `json:"instance_id" description:"Child instance ID."`
}

func buildDeleteInstance(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("DeleteInstance",
		deleteInstanceDescription,
		func(ctx context.Context, input deleteInstanceInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.InstanceID == "" {
				return fantasy.NewTextErrorResponse("instance_id is required"), nil
			}
			callerID := callerIDFromContext(ctx)
			scoped := NewScopedManager(mgr, callerID)
			if err := scoped.checkDescendant(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			logger.Info("tool call", "tool", "DeleteInstance", "target_instance", input.InstanceID)
			if err := mgr.DeleteInstance(input.InstanceID); err != nil {
				logger.Warn("DeleteInstance failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to delete instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s deleted.", input.InstanceID)), nil
		},
	)
}
