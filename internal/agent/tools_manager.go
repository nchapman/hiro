package agent

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

const maxAgentResultSize = 32 * 1024

func truncateResult(s string) string {
	if len(s) <= maxAgentResultSize {
		return s
	}
	return s[:maxAgentResultSize] + "\n\n(result truncated)"
}

// buildManagerTools returns tools that let an agent manage other agents.
// parentID is the ID of the agent these tools are being injected into.
// Tools are scoped so an agent can only manage its own descendants.
func (m *Manager) buildManagerTools(parentID string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		m.toolSpawnAgent(parentID),
		m.toolStartAgent(parentID),
		m.toolListAgents(parentID),
		m.toolSendMessage(parentID),
		m.toolStopAgent(parentID),
	}
}

// --- spawn_agent tool ---

type spawnAgentInput struct {
	Agent  string `json:"agent"  description:"The name of the agent definition to run (matches a directory name under agents/)."`
	Prompt string `json:"prompt" description:"A clear, self-contained description of the task. Do not assume the agent has any prior context."`
}

func (m *Manager) toolSpawnAgent(parentID string) fantasy.AgentTool {
	return fantasy.NewAgentTool("spawn_agent",
		"Spawn an ephemeral subagent to complete a task. The subagent runs the given prompt, returns the result, and is cleaned up. This call blocks until the subagent finishes.",
		func(ctx context.Context, input spawnAgentInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Agent == "" {
				return fantasy.NewTextErrorResponse("agent name is required"), nil
			}
			if input.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			m.logger.Info("spawning subagent",
				"agent", input.Agent,
				"parent", parentID,
			)

			result, err := m.SpawnSubagent(ctx, input.Agent, input.Prompt, parentID)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("subagent failed: %v", err)), nil
			}

			result = truncateResult(result)

			return fantasy.NewTextResponse(result), nil
		},
	)
}

// --- start_agent tool ---

type startAgentInput struct {
	Agent string `json:"agent" description:"The name of the agent definition to start (matches a directory name under agents/)."`
}

func (m *Manager) toolStartAgent(parentID string) fantasy.AgentTool {
	return fantasy.NewAgentTool("start_agent",
		"Start a persistent agent that stays running and can receive messages. Returns the agent's ID.",
		func(ctx context.Context, input startAgentInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Agent == "" {
				return fantasy.NewTextErrorResponse("agent name is required"), nil
			}

			id, err := m.StartAgent(ctx, input.Agent, parentID)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to start agent: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Agent %q started with ID: %s", input.Agent, id)), nil
		},
	)
}

// --- list_agents tool ---

func (m *Manager) toolListAgents(parentID string) fantasy.AgentTool {
	return fantasy.NewAgentTool("list_agents",
		"List your child agents — agents you have started or spawned.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			agents := m.ListChildren(parentID)
			if len(agents) == 0 {
				return fantasy.NewTextResponse("No child agents running."), nil
			}

			var sb strings.Builder
			for _, a := range agents {
				fmt.Fprintf(&sb, "- **%s** (id: %s, mode: %s)", a.Name, a.ID, a.Mode)
				if a.Description != "" {
					fmt.Fprintf(&sb, ": %s", a.Description)
				}
				sb.WriteString("\n")
			}
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}

// --- send_message tool ---

type sendMessageInput struct {
	AgentID string `json:"agent_id" description:"The ID of the agent to send a message to. Must be one of your child agents."`
	Message string `json:"message"  description:"The message to send to the agent."`
}

func (m *Manager) toolSendMessage(parentID string) fantasy.AgentTool {
	return fantasy.NewAgentTool("send_message",
		"Send a message to one of your child agents and get its response.",
		func(ctx context.Context, input sendMessageInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.AgentID == "" {
				return fantasy.NewTextErrorResponse("agent_id is required"), nil
			}
			if input.Message == "" {
				return fantasy.NewTextErrorResponse("message is required"), nil
			}

			if !m.IsDescendant(input.AgentID, parentID) {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("agent %q is not a descendant of this agent", input.AgentID)), nil
			}

			result, err := m.SendMessage(ctx, input.AgentID, input.Message, nil)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("send_message failed: %v", err)), nil
			}

			result = truncateResult(result)

			return fantasy.NewTextResponse(result), nil
		},
	)
}

// --- stop_agent tool ---

type stopAgentInput struct {
	AgentID string `json:"agent_id" description:"The ID of the agent to stop. Must be one of your child agents."`
}

func (m *Manager) toolStopAgent(parentID string) fantasy.AgentTool {
	return fantasy.NewAgentTool("stop_agent",
		"Stop one of your child agents and all of its descendants.",
		func(ctx context.Context, input stopAgentInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.AgentID == "" {
				return fantasy.NewTextErrorResponse("agent_id is required"), nil
			}

			if !m.IsDescendant(input.AgentID, parentID) {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("agent %q is not a descendant of this agent", input.AgentID)), nil
			}

			info, err := m.StopAgent(input.AgentID)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to stop agent: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Agent %q (%s) stopped.", info.Name, input.AgentID)), nil
		},
	)
}
