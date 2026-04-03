package inference

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
)

//go:embed todo_write.md
var todoWriteDescription string

type todoInput struct {
	Content    string `json:"content"     description:"What needs to be done."`
	Status     string `json:"status"      description:"Task status: pending, in_progress, or completed."`
	ActiveForm string `json:"active_form" description:"Present continuous form shown while in progress. Optional."`
}

func buildTodoTools(sessionDir string) []Tool {
	return wrapAll([]fantasy.AgentTool{
		fantasy.NewAgentTool("TodoWrite",
			todoWriteDescription,
			func(ctx context.Context, input struct {
				Todos []todoInput `json:"todos" description:"The complete updated todo list."`
			}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return handleTodoWrite(sessionDir, input.Todos)
			},
		),
	})
}

func handleTodoWrite(sessionDir string, items []todoInput) (fantasy.ToolResponse, error) {
	oldTodos, _ := config.ReadTodos(sessionDir)
	oldStatus := make(map[string]config.TodoStatus)
	for _, t := range oldTodos {
		oldStatus[t.Content] = t.Status
	}

	todos := make([]config.Todo, 0, len(items))
	for _, item := range items {
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
}
