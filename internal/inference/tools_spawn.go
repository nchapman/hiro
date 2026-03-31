package inference

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
)

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

// --- Spawn tool ---

type spawnInstanceInput struct {
	Agent  string `json:"agent"  description:"Agent definition name (directory under agents/)."`
	Prompt string `json:"prompt" description:"Task prompt. Required for ephemeral mode."`
	Mode   string `json:"mode"   description:"'ephemeral' (default), 'persistent', or 'coordinator'." default:"ephemeral"`
	Node   string `json:"node"   description:"Target node name. Omit for local."`
}

func buildSpawnTool(mgr ipc.HostManager, callerMode config.AgentMode, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("SpawnInstance",
		"Spawn a new agent instance. Ephemeral mode runs a prompt and returns the result. Persistent/coordinator mode creates a long-lived instance.",
		func(ctx context.Context, input spawnInstanceInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Agent == "" {
				return fantasy.NewTextErrorResponse("agent name is required"), nil
			}

			mode := input.Mode
			if mode == "" {
				mode = "ephemeral"
			}

			// Extract caller ID from context for parent lineage.
			callerID := callerIDFromContext(ctx)

			nodeID := ipc.NodeID(input.Node)

			switch config.AgentMode(mode) {
			case config.ModeEphemeral:
				if input.Prompt == "" {
					return fantasy.NewTextErrorResponse("prompt is required for ephemeral mode"), nil
				}
			case config.ModePersistent, config.ModeCoordinator:
				if callerMode != config.ModeCoordinator {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("only coordinator agents can spawn %s instances", mode)), nil
				}
			default:
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("invalid mode %q: must be ephemeral, persistent, or coordinator", mode)), nil
			}

			logger.Info("tool call", "tool", "SpawnInstance", "agent", input.Agent, "mode", mode)

			switch config.AgentMode(mode) {
			case config.ModeEphemeral:
				result, err := mgr.SpawnEphemeral(ctx, input.Agent, input.Prompt, callerID, nodeID, nil)
				if err != nil {
					logger.Warn("SpawnInstance failed", "agent", input.Agent, "mode", mode, "error", err)
					return fantasy.NewTextErrorResponse(fmt.Sprintf("instance failed: %v", err)), nil
				}
				return fantasy.NewTextResponse(truncateResult(result)), nil

			default: // persistent or coordinator
				id, err := mgr.CreateInstance(ctx, input.Agent, callerID, mode, nodeID)
				if err != nil {
					logger.Warn("SpawnInstance failed", "agent", input.Agent, "mode", mode, "error", err)
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to create instance: %v", err)), nil
				}
				return fantasy.NewTextResponse(
					fmt.Sprintf("Instance created from %q with ID: %s (mode: %s)", input.Agent, id, mode)), nil
			}
		},
	)
}

// --- Coordinator tools ---

func buildCoordinatorTools(mgr ipc.HostManager, logger *slog.Logger) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		buildResumeInstance(mgr, logger),
		buildListInstances(mgr, logger),
		buildListNodes(mgr, logger),
		buildSendMessage(mgr, logger),
		buildStopInstance(mgr, logger),
		buildDeleteInstance(mgr, logger),
	}
}

func buildListNodes(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("ListNodes",
		"List all nodes in the cluster.",
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
		"Resume a stopped instance with its persona and memory intact.",
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
		"List your direct child instances.",
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
		"Send a message to a running child instance and get its response.",
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
		"Stop an instance and its descendants. Data is preserved.",
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
		"Permanently delete an instance and all its data. Cannot be undone.",
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
