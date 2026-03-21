package agent

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/hub"
)

// buildTools creates the set of fantasy tools available to this agent.
func (a *Agent) buildTools() []fantasy.AgentTool {
	return []fantasy.AgentTool{
		a.toolListWorkers(),
		a.toolDelegateTask(),
	}
}

// --- list_workers tool ---

type listWorkersInput struct {
	Skill string `json:"skill,omitempty" description:"Optional skill to filter by. Leave empty to list all workers."`
}

func (a *Agent) toolListWorkers() fantasy.AgentTool {
	return fantasy.NewAgentTool("list_workers",
		"List all currently connected worker agents in the swarm, optionally filtered by skill.",
		func(ctx context.Context, input listWorkersInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			var workers []hub.Worker
			if input.Skill != "" {
				workers = a.swarm.FindWorkers(input.Skill)
			} else {
				workers = a.swarm.Workers()
			}

			if len(workers) == 0 {
				return fantasy.NewTextResponse("No workers currently connected."), nil
			}

			var sb strings.Builder
			for _, w := range workers {
				fmt.Fprintf(&sb, "- **%s** (id: %s): %s\n  Skills: %s\n",
					w.AgentName, w.ID, w.Description, strings.Join(w.Skills, ", "))
			}
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}

// --- delegate_task tool ---

type delegateTaskInput struct {
	Skill   string `json:"skill"   description:"The skill required for this task. Must match a skill advertised by a connected worker."`
	Prompt  string `json:"prompt"  description:"A clear, self-contained description of the task to delegate. Do not assume the worker has any prior context."`
	Context string `json:"context,omitempty" description:"Optional additional context to help the worker complete the task."`
}

func (a *Agent) toolDelegateTask() fantasy.AgentTool {
	return fantasy.NewAgentTool("delegate_task",
		"Delegate a task to a worker agent in the swarm that has the required skill. The task will be sent to an available worker and the result returned.",
		func(ctx context.Context, input delegateTaskInput, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			workers := a.swarm.FindWorkers(input.Skill)
			if len(workers) == 0 {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("No workers available with skill %q. Available workers: %s",
						input.Skill, a.describeAvailableSkills())), nil
			}

			// Pick the first available worker
			worker := workers[0]

			a.logger.Info("delegating task",
				"skill", input.Skill,
				"worker", worker.AgentName,
				"worker_id", worker.ID,
			)

			// Dispatch the task via the swarm's task dispatcher
			result, err := a.dispatchTask(ctx, worker, input)
			if err != nil {
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("Task delegation failed: %v", err)), nil
			}

			return fantasy.NewTextResponse(result), nil
		},
	)
}

func (a *Agent) describeAvailableSkills() string {
	workers := a.swarm.Workers()
	if len(workers) == 0 {
		return "(no workers connected)"
	}

	skillSet := make(map[string][]string) // skill -> agent names
	for _, w := range workers {
		for _, s := range w.Skills {
			skillSet[s] = append(skillSet[s], w.AgentName)
		}
	}

	var parts []string
	for skill, agents := range skillSet {
		parts = append(parts, fmt.Sprintf("%s (%s)", skill, strings.Join(agents, ", ")))
	}
	return strings.Join(parts, ", ")
}

// dispatchTask sends a task to a worker and waits for the result.
// For now, this creates a task record. The actual WebSocket dispatch
// will be wired up when the transport layer is connected.
func (a *Agent) dispatchTask(ctx context.Context, worker hub.Worker, input delegateTaskInput) (string, error) {
	if a.taskDispatcher != nil {
		return a.taskDispatcher(ctx, worker, input.Skill, input.Prompt, input.Context)
	}
	return "", fmt.Errorf("no task dispatcher configured — worker %q is connected but task routing is not yet wired", worker.AgentName)
}

// TaskDispatchFunc is called to dispatch a task to a worker.
// It should block until the task is complete and return the result.
type TaskDispatchFunc func(ctx context.Context, worker hub.Worker, skill, prompt, taskContext string) (string, error)

// taskDispatcher is set by the transport layer to enable actual task dispatch.
var _ TaskDispatchFunc // type assertion

// SetTaskDispatcher sets the function used to dispatch tasks to workers.
func (a *Agent) SetTaskDispatcher(fn TaskDispatchFunc) {
	a.taskDispatcher = fn
}
