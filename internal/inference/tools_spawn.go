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
	Agent  string `json:"agent"  description:"The name of the agent definition to run (matches a directory name under agents/)."`
	Prompt string `json:"prompt" description:"The task prompt. Required for ephemeral mode. For persistent/coordinator instances, use send_message after creation."`
	Mode   string `json:"mode"   description:"Instance mode: 'ephemeral' (default) runs the prompt and returns the result; 'persistent' or 'coordinator' creates a long-lived instance and returns its ID." default:"ephemeral"`
	Node   string `json:"node"   description:"Target node to run on. Omit or use 'home' for the local machine. Use list_nodes to see available nodes."`
}

func buildSpawnTool(mgr ipc.HostManager, callerMode config.AgentMode, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("spawn_instance",
		"Spawn a new instance from an agent definition. In ephemeral mode (default), runs a prompt and returns the result. In persistent or coordinator mode, creates a long-lived instance and returns its ID.",
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

			logger.Info("tool call", "tool", "spawn_instance", "agent", input.Agent, "mode", mode)

			switch config.AgentMode(mode) {
			case config.ModeEphemeral:
				result, err := mgr.SpawnEphemeral(ctx, input.Agent, input.Prompt, callerID, nodeID, nil)
				if err != nil {
					logger.Warn("spawn_instance failed", "agent", input.Agent, "mode", mode, "error", err)
					return fantasy.NewTextErrorResponse(fmt.Sprintf("instance failed: %v", err)), nil
				}
				return fantasy.NewTextResponse(truncateResult(result)), nil

			default: // persistent or coordinator
				id, err := mgr.CreateInstance(ctx, input.Agent, callerID, mode, nodeID)
				if err != nil {
					logger.Warn("spawn_instance failed", "agent", input.Agent, "mode", mode, "error", err)
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
	return fantasy.NewAgentTool("list_nodes",
		"List all nodes in the cluster. Shows each node's name, status, capacity, and active worker count. Use node names with spawn_instance to run agents on specific machines.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			logger.Debug("tool call", "tool", "list_nodes")
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
	InstanceID string `json:"instance_id" description:"The ID of a stopped instance to resume."`
}

func buildResumeInstance(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("resume_instance",
		"Resume a stopped instance. Creates a new session within it. Picks up where it left off with its memory and identity.",
		func(ctx context.Context, input resumeInstanceInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.InstanceID == "" {
				return fantasy.NewTextErrorResponse("instance_id is required"), nil
			}
			callerID := callerIDFromContext(ctx)
			scoped := NewScopedManager(mgr, callerID)
			if err := scoped.checkDescendant(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			logger.Info("tool call", "tool", "resume_instance", "target_instance", input.InstanceID)
			if err := mgr.StartInstance(ctx, input.InstanceID); err != nil {
				logger.Warn("resume_instance failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to resume instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s resumed.", input.InstanceID)), nil
		},
	)
}

func buildListInstances(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("list_instances",
		"List your direct child instances with their name, mode, and status.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			logger.Debug("tool call", "tool", "list_instances")
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
	InstanceID string `json:"instance_id" description:"The ID of the instance to send a message to. Must be one of your child instances."`
	Message    string `json:"message"     description:"The message to send to the instance."`
}

func buildSendMessage(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("send_message",
		"Send a message to a running child instance and get its response. Blocks until the instance replies.",
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
			logger.Info("tool call", "tool", "send_message", "target_instance", input.InstanceID)
			result, err := mgr.SendMessage(ctx, input.InstanceID, input.Message, nil)
			if err != nil {
				logger.Warn("send_message failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("send_message failed: %v", err)), nil
			}
			return fantasy.NewTextResponse(truncateResult(result)), nil
		},
	)
}

type stopInstanceInput struct {
	InstanceID string `json:"instance_id" description:"The ID of the instance to stop. Must be one of your child instances."`
}

func buildStopInstance(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("stop_instance",
		"Stop an instance and its descendants. Data is preserved — use resume_instance to restart, or delete_instance to remove permanently.",
		func(ctx context.Context, input stopInstanceInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.InstanceID == "" {
				return fantasy.NewTextErrorResponse("instance_id is required"), nil
			}
			callerID := callerIDFromContext(ctx)
			scoped := NewScopedManager(mgr, callerID)
			if err := scoped.checkDescendant(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			logger.Info("tool call", "tool", "stop_instance", "target_instance", input.InstanceID)
			if _, err := mgr.StopInstance(input.InstanceID); err != nil {
				logger.Warn("stop_instance failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to stop instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s stopped.", input.InstanceID)), nil
		},
	)
}

type deleteInstanceInput struct {
	InstanceID string `json:"instance_id" description:"The ID of the instance to delete. Must be one of your child instances."`
}

func buildDeleteInstance(mgr ipc.HostManager, logger *slog.Logger) fantasy.AgentTool {
	return fantasy.NewAgentTool("delete_instance",
		"Permanently delete an instance and its descendants, removing all data. Stops it first if running. Cannot be undone.",
		func(ctx context.Context, input deleteInstanceInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.InstanceID == "" {
				return fantasy.NewTextErrorResponse("instance_id is required"), nil
			}
			callerID := callerIDFromContext(ctx)
			scoped := NewScopedManager(mgr, callerID)
			if err := scoped.checkDescendant(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			logger.Info("tool call", "tool", "delete_instance", "target_instance", input.InstanceID)
			if err := mgr.DeleteInstance(input.InstanceID); err != nil {
				logger.Warn("delete_instance failed", "target_instance", input.InstanceID, "error", err)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to delete instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s deleted.", input.InstanceID)), nil
		},
	)
}
