package agent

import (
	"context"
	"fmt"

	"github.com/nchapman/hivebot/internal/ipc"
)

// localHost wraps a Manager to implement ipc.AgentHost for in-process use.
// It enforces descendant authorization for scoped operations.
type localHost struct {
	mgr      *Manager
	parentID string // the agent this host is scoped to
}

// newLocalHost creates an in-process AgentHost scoped to the given parent agent.
func newLocalHost(mgr *Manager, parentID string) *localHost {
	return &localHost{mgr: mgr, parentID: parentID}
}

func (h *localHost) SpawnAgent(ctx context.Context, agentName, prompt string, onDelta func(string) error) (string, error) {
	return h.mgr.SpawnSubagent(ctx, agentName, prompt, h.parentID)
}

func (h *localHost) StartAgent(ctx context.Context, agentName string) (string, error) {
	return h.mgr.StartAgent(ctx, agentName, h.parentID)
}

func (h *localHost) SendMessage(ctx context.Context, agentID, message string, onDelta func(string) error) (string, error) {
	if !h.mgr.IsDescendant(agentID, h.parentID) {
		return "", fmt.Errorf("agent %q is not a descendant of this agent", agentID)
	}
	return h.mgr.SendMessage(ctx, agentID, message, onDelta)
}

func (h *localHost) StopAgent(ctx context.Context, agentID string) error {
	if !h.mgr.IsDescendant(agentID, h.parentID) {
		return fmt.Errorf("agent %q is not a descendant of this agent", agentID)
	}
	_, err := h.mgr.StopAgent(agentID)
	return err
}

func (h *localHost) ListAgents(ctx context.Context) ([]ipc.AgentInfo, error) {
	children := h.mgr.ListChildren(h.parentID)
	result := make([]ipc.AgentInfo, len(children))
	for i, c := range children {
		result[i] = ipc.AgentInfo{
			ID:          c.ID,
			Name:        c.Name,
			Mode:        string(c.Mode),
			Description: c.Description,
			ParentID:    c.ParentID,
		}
	}
	return result, nil
}

func (h *localHost) GetSecrets(ctx context.Context) (names []string, env []string, err error) {
	if h.mgr.cp == nil {
		return nil, nil, nil
	}
	return h.mgr.cp.SecretNames(), h.mgr.cp.SecretEnv(), nil
}
