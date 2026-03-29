package inference

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/cluster"
	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
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

func buildSpawnTool(mgr ipc.HostManager, callerMode config.AgentMode) fantasy.AgentTool {
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

			nodeID := cluster.NodeID(input.Node)

			switch config.AgentMode(mode) {
			case config.ModeEphemeral:
				if input.Prompt == "" {
					return fantasy.NewTextErrorResponse("prompt is required for ephemeral mode"), nil
				}
				result, err := mgr.SpawnEphemeral(ctx, input.Agent, input.Prompt, callerID, nodeID, nil)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("instance failed: %v", err)), nil
				}
				return fantasy.NewTextResponse(truncateResult(result)), nil

			case config.ModePersistent, config.ModeCoordinator:
				if callerMode != config.ModeCoordinator {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("only coordinator agents can spawn %s instances", mode)), nil
				}
				id, err := mgr.CreateInstance(ctx, input.Agent, callerID, mode, nodeID)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to create instance: %v", err)), nil
				}
				return fantasy.NewTextResponse(
					fmt.Sprintf("Instance created from %q with ID: %s (mode: %s)", input.Agent, id, mode)), nil

			default:
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("invalid mode %q: must be ephemeral, persistent, or coordinator", mode)), nil
			}
		},
	)
}

// --- Coordinator tools ---

func buildCoordinatorTools(mgr ipc.HostManager) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		buildResumeInstance(mgr),
		buildListInstances(mgr),
		buildListNodes(mgr),
		buildSendMessage(mgr),
		buildStopInstance(mgr),
		buildDeleteInstance(mgr),
	}
}

func buildListNodes(mgr ipc.HostManager) fantasy.AgentTool {
	return fantasy.NewAgentTool("list_nodes",
		"List all nodes in the cluster. Shows each node's name, status, capacity, and active worker count. Use node names with spawn_instance to run agents on specific machines.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
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

func buildResumeInstance(mgr ipc.HostManager) fantasy.AgentTool {
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
			if err := mgr.StartInstance(ctx, input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to resume instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s resumed.", input.InstanceID)), nil
		},
	)
}

func buildListInstances(mgr ipc.HostManager) fantasy.AgentTool {
	return fantasy.NewAgentTool("list_instances",
		"List your direct child instances with their name, mode, and status.",
		func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
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

func buildSendMessage(mgr ipc.HostManager) fantasy.AgentTool {
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
			result, err := mgr.SendMessage(ctx, input.InstanceID, input.Message, nil)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("send_message failed: %v", err)), nil
			}
			return fantasy.NewTextResponse(truncateResult(result)), nil
		},
	)
}

type stopInstanceInput struct {
	InstanceID string `json:"instance_id" description:"The ID of the instance to stop. Must be one of your child instances."`
}

func buildStopInstance(mgr ipc.HostManager) fantasy.AgentTool {
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
			if _, err := mgr.StopInstance(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to stop instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s stopped.", input.InstanceID)), nil
		},
	)
}

type deleteInstanceInput struct {
	InstanceID string `json:"instance_id" description:"The ID of the instance to delete. Must be one of your child instances."`
}

func buildDeleteInstance(mgr ipc.HostManager) fantasy.AgentTool {
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
			if err := mgr.DeleteInstance(input.InstanceID); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to delete instance: %v", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Instance %s deleted.", input.InstanceID)), nil
		},
	)
}

// --- Memory tools ---

func buildMemoryTools(instanceDir string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		fantasy.NewAgentTool("memory_read",
			"Read your persistent memory file. Contains facts and context you've chosen to retain across conversations. Read before writing to avoid overwriting entries.",
			func(ctx context.Context, input struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				content, err := config.ReadMemoryFile(instanceDir)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to read memory: %v", err)), nil
				}
				if content == "" {
					return fantasy.NewTextResponse("No memories stored yet."), nil
				}
				return fantasy.NewTextResponse(content), nil
			},
		),
		fantasy.NewAgentTool("memory_write",
			"Overwrite your persistent memory file. Read first to avoid losing existing entries. Changes appear in your system prompt from the next turn onward.",
			func(ctx context.Context, input struct {
				Content string `json:"content" description:"The full new contents of your memory file."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.Content == "" {
					return fantasy.NewTextErrorResponse("content is required"), nil
				}
				if err := config.WriteMemoryFile(instanceDir, input.Content); err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to write memory: %v", err)), nil
				}
				return fantasy.NewTextResponse(
					fmt.Sprintf("Memory updated (%d bytes). Changes will be reflected in your system prompt on the next turn.", len(input.Content))), nil
			},
		),
	}
}

// --- Todo tools ---

func buildTodoTools(sessionDir string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		fantasy.NewAgentTool("todos",
			"Replace your task list. Send the full updated list — add, remove, reorder, and change statuses in one call. Use for multi-step work. Tasks appear in your system prompt.",
			func(ctx context.Context, input struct {
				Todos []struct {
					Content    string `json:"content"     description:"What needs to be done."`
					Status     string `json:"status"      description:"Task status: pending, in_progress, or completed."`
					ActiveForm string `json:"active_form" description:"Present continuous form shown while in progress. Optional."`
				} `json:"todos" description:"The complete updated todo list."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				oldTodos, _ := config.ReadTodos(sessionDir)
				oldStatus := make(map[string]config.TodoStatus)
				for _, t := range oldTodos {
					oldStatus[t.Content] = t.Status
				}

				todos := make([]config.Todo, 0, len(input.Todos))
				for _, item := range input.Todos {
					switch config.TodoStatus(item.Status) {
					case config.TodoStatusPending, config.TodoStatusInProgress, config.TodoStatusCompleted:
					default:
						return fantasy.NewTextErrorResponse(
							fmt.Sprintf("invalid status %q for %q", item.Status, item.Content)), nil
					}
					todos = append(todos, config.Todo{
						Content:    item.Content,
						Status:     config.TodoStatus(item.Status),
						ActiveForm: item.ActiveForm,
					})
				}

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
					return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to write todos: %v", err)), nil
				}

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
		),
	}
}

// --- History tools ---

func buildHistoryTools(pdb *platformdb.DB, sessionID string) []fantasy.AgentTool {
	return []fantasy.AgentTool{
		fantasy.NewAgentTool("history_search",
			"Search your conversation history for past messages and summaries.",
			func(ctx context.Context, input struct {
				Query string `json:"query" description:"Search query (full-text search)."`
				Scope string `json:"scope" description:"Where to search: 'messages', 'summaries', or 'all'. Default: 'all'." default:"all"`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.Query == "" {
					return fantasy.NewTextErrorResponse("query is required"), nil
				}
				scope := input.Scope
				if scope == "" {
					scope = "all"
				}

				var results []platformdb.SearchResult
				var err error
				switch scope {
				case "messages":
					results, err = pdb.SearchMessages(sessionID, input.Query, 20)
				case "summaries":
					results, err = pdb.SearchSummaries(sessionID, input.Query, 20)
				default:
					results, err = pdb.Search(sessionID, input.Query, 20)
				}
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("search failed: %v", err)), nil
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
		),
		fantasy.NewAgentTool("history_recall",
			"Expand a conversation summary to see its full content and the lower-level summaries or messages it was created from.",
			func(ctx context.Context, input struct {
				SummaryID string `json:"summary_id" description:"The ID of a summary to expand (e.g. 'sum_abc123')."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				if input.SummaryID == "" {
					return fantasy.NewTextErrorResponse("summary_id is required"), nil
				}

				sum, err := pdb.GetSummary(input.SummaryID)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("summary not found: %v", err)), nil
				}

				var sb strings.Builder
				fmt.Fprintf(&sb, "## Summary %s (depth %d, %s)\n\n", sum.ID, sum.Depth, sum.Kind)
				fmt.Fprintf(&sb, "Time range: %s to %s\n",
					sum.EarliestAt.Format("2006-01-02 15:04"),
					sum.LatestAt.Format("2006-01-02 15:04"))
				fmt.Fprintf(&sb, "Compression: %d tokens → %d tokens\n\n", sum.SourceTokens, sum.Tokens)
				sb.WriteString(sum.Content)

				if sum.Kind == "leaf" {
					msgIDs, err := pdb.GetSummarySourceMessages(sum.ID)
					if err == nil && len(msgIDs) > 0 {
						msgs, err := pdb.GetMessages(msgIDs)
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
					childIDs, err := pdb.GetSummaryChildren(sum.ID)
					if err == nil && len(childIDs) > 0 {
						sb.WriteString("\n\n---\n### Child Summaries\n\n")
						for _, cid := range childIDs {
							child, err := pdb.GetSummary(cid)
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
		),
	}
}

// --- Skill tool ---

func buildSkillTool(cfg *config.AgentConfig, allowedDirs []string) fantasy.AgentTool {
	return fantasy.NewAgentTool("use_skill",
		"Activate a skill to get its full instructions and required formats. You MUST call this before performing any task that matches a skill.",
		func(ctx context.Context, input struct {
			Name string `json:"name" description:"The name of the skill to activate."`
		}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Name == "" {
				return fantasy.NewTextErrorResponse("skill name is required"), nil
			}

			var skill *config.SkillConfig
			for i := range cfg.Skills {
				if cfg.Skills[i].Name == input.Name {
					skill = &cfg.Skills[i]
					break
				}
			}
			if skill == nil {
				names := make([]string, len(cfg.Skills))
				for i, s := range cfg.Skills {
					names[i] = s.Name
				}
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("skill %q not found. Available: %s", input.Name, strings.Join(names, ", "))), nil
			}

			realPath := skill.Path
			if resolved, err := filepath.EvalSymlinks(skill.Path); err == nil {
				realPath = resolved
			}
			if !isUnderAllowedDir(realPath, allowedDirs) {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("skill %q path is outside allowed directories", input.Name)), nil
			}

			parsed, err := config.ParseMarkdownFile(realPath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error reading skill file: %v", err)), nil
			}

			var result strings.Builder
			result.WriteString(parsed.Body)

			if filepath.Base(realPath) == "SKILL.md" {
				skillDir := filepath.Dir(realPath)
				entries, err := os.ReadDir(skillDir)
				if err == nil {
					const maxResources = 50
					var resources []string
					truncated := false
					for _, e := range entries {
						if len(resources) >= maxResources {
							truncated = true
							break
						}
						name := e.Name()
						if name == "SKILL.md" {
							continue
						}
						if e.IsDir() {
							resources = append(resources, name+"/")
							subEntries, subErr := os.ReadDir(filepath.Join(skillDir, name))
							if subErr == nil {
								for _, sub := range subEntries {
									if len(resources) >= maxResources {
										truncated = true
										break
									}
									subName := name + "/" + sub.Name()
									if sub.IsDir() {
										subName += "/"
									}
									resources = append(resources, "  "+subName)
								}
							}
						} else {
							resources = append(resources, name)
						}
					}
					if truncated {
						resources = append(resources, "... (truncated)")
					}
					if len(resources) > 0 {
						result.WriteString("\n\n## Bundled Resources\n\n")
						for _, r := range resources {
							fmt.Fprintf(&result, "- %s\n", r)
						}
					}
				}
			}

			return fantasy.NewTextResponse(result.String()), nil
		},
	)
}

func isUnderAllowedDir(path string, allowedDirs []string) bool {
	cleanPath := filepath.Clean(path)
	hasAny := false
	for _, dir := range allowedDirs {
		if dir == "" {
			continue
		}
		hasAny = true
		prefix := filepath.Clean(dir) + string(filepath.Separator)
		if strings.HasPrefix(cleanPath, prefix) {
			return true
		}
	}
	return !hasAny
}
