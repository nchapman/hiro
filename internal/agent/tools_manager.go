package agent

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/history"
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

// buildHistoryTools returns tools for searching and exploring conversation history.
// These are only injected into agents with a history engine.
func buildHistoryTools(engine *history.Engine) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		toolHistorySearch(engine),
		toolHistoryRecall(engine),
	}
}

// buildMemoryTools returns tools for reading and writing agent memory.
// instanceDir is the agent's instance directory containing memory.md.
func buildMemoryTools(instanceDir string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		toolMemoryRead(instanceDir),
		toolMemoryWrite(instanceDir),
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

// --- history_search tool ---

type historySearchInput struct {
	Query string `json:"query" description:"Search query (full-text search). Use keywords that might appear in past messages or summaries."`
	Scope string `json:"scope" description:"Where to search: 'messages' (raw messages only), 'summaries' (compacted summaries only), or 'all' (both). Default: 'all'." default:"all"`
}

func toolHistorySearch(engine *history.Engine) fantasy.AgentTool {
	return fantasy.NewAgentTool("history_search",
		"Search your conversation history for past messages and summaries. Use this when you need to recall something discussed earlier that may have been compacted out of your active context.",
		func(ctx context.Context, input historySearchInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}
			scope := input.Scope
			if scope == "" {
				scope = "all"
			}

			store := engine.Store()
			var results []history.SearchResult
			var err error

			switch scope {
			case "messages":
				results, err = store.SearchMessages(input.Query, 20)
			case "summaries":
				results, err = store.SearchSummaries(input.Query, 20)
			default:
				results, err = store.Search(input.Query, 20)
			}
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("search failed: %v", err)), nil
			}

			if len(results) == 0 {
				return fantasy.NewTextResponse("No results found."), nil
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "Found %d results:\n\n", len(results))
			for _, r := range results {
				fmt.Fprintf(&sb, "- [%s:%s] %s\n", r.Type, r.ID, r.Snippet)
			}
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}

// --- history_recall tool ---

type historyRecallInput struct {
	SummaryID string `json:"summary_id" description:"The ID of a summary to expand (e.g. 'sum_abc123'). Get these from history_search results."`
}

func toolHistoryRecall(engine *history.Engine) fantasy.AgentTool {
	return fantasy.NewAgentTool("history_recall",
		"Drill into a conversation summary to see more detail. Returns the summary's full content plus its children (the lower-level summaries or original messages it was created from).",
		func(ctx context.Context, input historyRecallInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.SummaryID == "" {
				return fantasy.NewTextErrorResponse("summary_id is required"), nil
			}

			store := engine.Store()

			sum, err := store.GetSummary(input.SummaryID)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("summary not found: %v", err)), nil
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "## Summary %s (depth %d, %s)\n\n",
				sum.ID, sum.Depth, sum.Kind)
			fmt.Fprintf(&sb, "Time range: %s to %s\n",
				sum.EarliestAt.Format("2006-01-02 15:04"),
				sum.LatestAt.Format("2006-01-02 15:04"))
			fmt.Fprintf(&sb, "Compression: %d tokens → %d tokens\n\n",
				sum.SourceTokens, sum.Tokens)
			sb.WriteString(sum.Content)

			// Show children
			if sum.Kind == "leaf" {
				// Show source messages
				msgIDs, err := store.GetSummarySourceMessages(sum.ID)
				if err == nil && len(msgIDs) > 0 {
					msgs, err := store.GetMessages(msgIDs)
					if err == nil {
						sb.WriteString("\n\n---\n### Source Messages\n\n")
						for _, m := range msgs {
							fmt.Fprintf(&sb, "[%s] **%s**: %s\n\n",
								m.CreatedAt.Format("15:04:05"), m.Role,
								truncateResult(m.Content))
						}
					}
				}
			} else {
				// Show child summaries
				childIDs, err := store.GetSummaryChildren(sum.ID)
				if err == nil && len(childIDs) > 0 {
					sb.WriteString("\n\n---\n### Child Summaries\n\n")
					for _, cid := range childIDs {
						child, err := store.GetSummary(cid)
						if err != nil {
							continue
						}
						fmt.Fprintf(&sb, "**%s** (depth %d, %s to %s):\n%s\n\n",
							child.ID, child.Depth,
							child.EarliestAt.Format("2006-01-02 15:04"),
							child.LatestAt.Format("2006-01-02 15:04"),
							child.Content)
					}
				}
			}

			return fantasy.NewTextResponse(truncateResult(sb.String())), nil
		},
	)
}

// --- memory_read tool ---

func toolMemoryRead(instanceDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool("memory_read",
		"Read your persistent memory. Returns the current contents of your memory file, which contains facts, preferences, and knowledge you've chosen to remember across conversations.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			content, err := config.ReadMemoryFile(instanceDir)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to read memory: %v", err)), nil
			}
			if content == "" {
				return fantasy.NewTextResponse("No memories stored yet."), nil
			}
			return fantasy.NewTextResponse(content), nil
		},
	)
}

// --- memory_write tool ---

type memoryWriteInput struct {
	Content string `json:"content" description:"The full new contents of your memory file. This overwrites the entire file, so include everything you want to remember. Read your current memory first to avoid losing existing entries."`
}

func toolMemoryWrite(instanceDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool("memory_write",
		"Write your persistent memory. Overwrites the entire memory file with the provided content. Always read your current memory first, then include both existing and new entries. Your memories are included in your system prompt on every turn, so they're always available to you.",
		func(ctx context.Context, input memoryWriteInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Content == "" {
				return fantasy.NewTextErrorResponse("content is required"), nil
			}

			if err := config.WriteMemoryFile(instanceDir, input.Content); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to write memory: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Memory updated (%d bytes). Changes will be reflected in your system prompt on the next turn.", len(input.Content))), nil
		},
	)
}
