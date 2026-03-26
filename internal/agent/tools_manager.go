package agent

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/history"
	"github.com/nchapman/hivebot/internal/ipc"
)

const maxAgentResultSize = 32 * 1024

func truncateResult(s string) string {
	if len(s) <= maxAgentResultSize {
		return s
	}
	return s[:maxAgentResultSize] + "\n\n(result truncated)"
}

// BuildSpawnTool returns the spawn_session tool, available to all agents.
// callerMode gates non-coordinator agents to ephemeral mode only.
func BuildSpawnTool(host ipc.AgentHost, callerMode config.AgentMode) fantasy.AgentTool {
	return toolSpawnSession(host, callerMode)
}

// BuildCoordinatorTools returns tools for managing session lifecycles.
// Only coordinator-mode agents should receive these.
func BuildCoordinatorTools(host ipc.AgentHost) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		toolResumeSession(host),
		toolListSessions(host),
		toolSendMessage(host),
		toolStopSession(host),
		toolDeleteSession(host),
	}
}

// BuildHistoryTools returns tools for searching and exploring conversation history.
// These are only injected into agents with a history engine.
func BuildHistoryTools(engine *history.Engine) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		toolHistorySearch(engine),
		toolHistoryRecall(engine),
	}
}

// BuildMemoryTools returns tools for reading and writing agent memory.
// sessionDir is the agent's session directory containing memory.md.
func BuildMemoryTools(sessionDir string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		toolMemoryRead(sessionDir),
		toolMemoryWrite(sessionDir),
	}
}

// BuildTodoTools returns a tool for managing the agent's todo list.
func BuildTodoTools(sessionDir string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		toolTodos(sessionDir),
	}
}

// --- spawn_session tool ---

type spawnSessionInput struct {
	Agent  string `json:"agent"  description:"The name of the agent definition to run (matches a directory name under agents/)."`
	Prompt string `json:"prompt" description:"The task prompt. Required for ephemeral mode. For persistent/coordinator sessions, use send_message after creation."`
	Mode   string `json:"mode"   description:"Session mode: 'ephemeral' (default) runs the prompt and returns the result; 'persistent' or 'coordinator' creates a long-lived session and returns its ID." default:"ephemeral"`
}

func toolSpawnSession(host ipc.AgentHost, callerMode config.AgentMode) fantasy.AgentTool {
	return fantasy.NewAgentTool("spawn_session",
		"Spawn a new session from an agent definition. In ephemeral mode (default), runs a prompt and returns the result. In persistent or coordinator mode, creates a long-lived session and returns its ID.",
		func(ctx context.Context, input spawnSessionInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Agent == "" {
				return fantasy.NewTextErrorResponse("agent name is required"), nil
			}

			mode := input.Mode
			if mode == "" {
				mode = "ephemeral"
			}

			switch config.AgentMode(mode) {
			case config.ModeEphemeral:
				if input.Prompt == "" {
					return fantasy.NewTextErrorResponse("prompt is required for ephemeral mode"), nil
				}
				result, err := host.SpawnSession(ctx, input.Agent, input.Prompt, nil)
				if err != nil {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("session failed: %v", err)), nil
				}
				return fantasy.NewTextResponse(truncateResult(result)), nil

			case config.ModePersistent, config.ModeCoordinator:
				if callerMode != config.ModeCoordinator {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("only coordinator agents can spawn %s sessions", mode)), nil
				}
				id, err := host.CreateSession(ctx, input.Agent, mode)
				if err != nil {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("failed to create session: %v", err)), nil
				}
				return fantasy.NewTextResponse(
					fmt.Sprintf("Session created from %q with ID: %s (mode: %s)", input.Agent, id, mode)), nil

			default:
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("invalid mode %q: must be ephemeral, persistent, or coordinator", mode)), nil
			}
		},
	)
}

// --- resume_session tool ---

type resumeSessionInput struct {
	SessionID string `json:"session_id" description:"The ID of a stopped session to resume."`
}

func toolResumeSession(host ipc.AgentHost) fantasy.AgentTool {
	return fantasy.NewAgentTool("resume_session",
		"Resume a stopped session. Picks up where it left off with its previous memory, history, and todos.",
		func(ctx context.Context, input resumeSessionInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.SessionID == "" {
				return fantasy.NewTextErrorResponse("session_id is required"), nil
			}

			err := host.StartSession(ctx, input.SessionID)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to resume session: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Session %s resumed.", input.SessionID)), nil
		},
	)
}

// --- list_sessions tool ---

func toolListSessions(host ipc.AgentHost) fantasy.AgentTool {
	return fantasy.NewAgentTool("list_sessions",
		"List your direct child sessions with their name, mode, and status.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			sessions, err := host.ListSessions(ctx)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to list sessions: %v", err)), nil
			}
			if len(sessions) == 0 {
				return fantasy.NewTextResponse("No child sessions."), nil
			}

			var sb strings.Builder
			for _, s := range sessions {
				fmt.Fprintf(&sb, "- **%s** (id: %s, mode: %s, status: %s)", s.Name, s.ID, s.Mode, s.Status)
				if s.Description != "" {
					fmt.Fprintf(&sb, ": %s", s.Description)
				}
				sb.WriteString("\n")
			}
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}

// --- send_message tool ---

type sendMessageInput struct {
	SessionID string `json:"session_id" description:"The ID of the session to send a message to. Must be one of your child sessions."`
	Message   string `json:"message"    description:"The message to send to the session."`
}

func toolSendMessage(host ipc.AgentHost) fantasy.AgentTool {
	return fantasy.NewAgentTool("send_message",
		"Send a message to a running child session and get its response. Blocks until the session replies.",
		func(ctx context.Context, input sendMessageInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.SessionID == "" {
				return fantasy.NewTextErrorResponse("session_id is required"), nil
			}
			if input.Message == "" {
				return fantasy.NewTextErrorResponse("message is required"), nil
			}

			result, err := host.SendMessage(ctx, input.SessionID, input.Message, nil)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("send_message failed: %v", err)), nil
			}

			return fantasy.NewTextResponse(truncateResult(result)), nil
		},
	)
}

// --- stop_session tool ---

type stopSessionInput struct {
	SessionID string `json:"session_id" description:"The ID of the session to stop. Must be one of your child sessions."`
}

func toolStopSession(host ipc.AgentHost) fantasy.AgentTool {
	return fantasy.NewAgentTool("stop_session",
		"Stop a session and its descendants. Data is preserved — use resume_session to restart, or delete_session to remove permanently.",
		func(ctx context.Context, input stopSessionInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.SessionID == "" {
				return fantasy.NewTextErrorResponse("session_id is required"), nil
			}

			err := host.StopSession(ctx, input.SessionID)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to stop session: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Session %s stopped.", input.SessionID)), nil
		},
	)
}

// --- delete_session tool ---

type deleteSessionInput struct {
	SessionID string `json:"session_id" description:"The ID of the session to delete. Must be one of your child sessions."`
}

func toolDeleteSession(host ipc.AgentHost) fantasy.AgentTool {
	return fantasy.NewAgentTool("delete_session",
		"Permanently delete a session and its descendants, removing all data. Stops it first if running. Cannot be undone.",
		func(ctx context.Context, input deleteSessionInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.SessionID == "" {
				return fantasy.NewTextErrorResponse("session_id is required"), nil
			}

			err := host.DeleteSession(ctx, input.SessionID)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to delete session: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Session %s deleted.", input.SessionID)), nil
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
		"Search your conversation history for past messages and summaries. Use when you need to recall something that may have been compacted out of your active context.",
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
		"Expand a conversation summary to see its full content and the lower-level summaries or messages it was created from.",
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

func toolMemoryRead(sessionDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool("memory_read",
		"Read your persistent memory file. Contains facts and context you've chosen to retain across conversations. Read before writing to avoid overwriting entries.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			content, err := config.ReadMemoryFile(sessionDir)
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

func toolMemoryWrite(sessionDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool("memory_write",
		"Overwrite your persistent memory file. Read first to avoid losing existing entries. Changes appear in your system prompt from the next turn onward.",
		func(ctx context.Context, input memoryWriteInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Content == "" {
				return fantasy.NewTextErrorResponse("content is required"), nil
			}

			if err := config.WriteMemoryFile(sessionDir, input.Content); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to write memory: %v", err)), nil
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Memory updated (%d bytes). Changes will be reflected in your system prompt on the next turn.", len(input.Content))), nil
		},
	)
}

// --- todos tool ---

type todosInput struct {
	Todos []todoItem `json:"todos" description:"The complete updated todo list. Send the full list each time — items not included will be removed."`
}

type todoItem struct {
	Content    string `json:"content"     description:"What needs to be done (imperative form, e.g. 'Set up database schema')."`
	Status     string `json:"status"      description:"Task status: pending, in_progress, or completed."`
	ActiveForm string `json:"active_form" description:"Present continuous form shown while in progress (e.g. 'Setting up database schema'). Optional."`
}

func toolTodos(sessionDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool("todos",
		"Replace your task list. Send the full updated list — add, remove, reorder, and change statuses in one call. Use for multi-step work. Tasks appear in your system prompt.",
		func(ctx context.Context, input todosInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			// Read old todos for change tracking
			oldTodos, _ := config.ReadTodos(sessionDir)
			oldStatus := make(map[string]config.TodoStatus)
			for _, t := range oldTodos {
				oldStatus[t.Content] = t.Status
			}

			// Validate and convert
			todos := make([]config.Todo, 0, len(input.Todos))
			for _, item := range input.Todos {
				switch config.TodoStatus(item.Status) {
				case config.TodoStatusPending, config.TodoStatusInProgress, config.TodoStatusCompleted:
				default:
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("invalid status %q for %q: must be pending, in_progress, or completed", item.Status, item.Content)), nil
				}
				todos = append(todos, config.Todo{
					Content:    item.Content,
					Status:     config.TodoStatus(item.Status),
					ActiveForm: item.ActiveForm,
				})
			}

			// Track changes
			var justCompleted []string
			var justStarted string
			completed := 0
			for _, t := range todos {
				if t.Status == config.TodoStatusCompleted {
					completed++
					if oldStatus[t.Content] != config.TodoStatusCompleted {
						justCompleted = append(justCompleted, t.Content)
					}
				}
				if t.Status == config.TodoStatusInProgress && oldStatus[t.Content] != config.TodoStatusInProgress {
					justStarted = t.Content
				}
			}

			if err := config.WriteTodos(sessionDir, todos); err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("failed to write todos: %v", err)), nil
			}

			// Build response
			var sb strings.Builder
			fmt.Fprintf(&sb, "Tasks updated: %d/%d completed.", completed, len(todos))
			if len(justCompleted) > 0 {
				fmt.Fprintf(&sb, " Completed: %s.", strings.Join(justCompleted, ", "))
			}
			if justStarted != "" {
				fmt.Fprintf(&sb, " Started: %s.", justStarted)
			}
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}
