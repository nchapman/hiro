package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/cluster"
	"github.com/nchapman/hivebot/internal/inference"
	"github.com/nchapman/hivebot/internal/ipc"
)

// SendMessage sends a message to a running instance and streams the response.
// onEvent is called for each streaming event; it may be nil. Calls are serialized
// per instance to prevent conversation corruption.
func (m *Manager) SendMessage(ctx context.Context, instanceID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessageWithFiles(ctx, instanceID, message, nil, onEvent)
}

// SendMessageWithFiles is like SendMessage but includes file attachments
// (images, PDFs, text documents) passed to the inference loop as fantasy.FileParts.
func (m *Manager) SendMessageWithFiles(ctx context.Context, instanceID, message string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	// Cycle detection: prevent re-entrant deadlocks when coordinator tools
	// send messages in a loop (A → SendMessage(B) → B sends back to A).
	if inference.IsInCallChain(ctx, instanceID) {
		return "", fmt.Errorf("circular message dependency: instance %s is already awaiting a response in this call chain", instanceID)
	}

	inst := m.getInstance(instanceID)
	if inst == nil {
		return "", fmt.Errorf("instance %q not found", instanceID)
	}

	inst.mu.Lock()
	defer inst.mu.Unlock()

	// Status check inside lock to avoid race with softStop/watchWorker.
	if inst.info.Status == InstanceStatusStopped {
		return "", fmt.Errorf("instance %q is stopped", instanceID)
	}

	// Add this instance to the call chain and set the caller ID for tool scoping.
	ctx = inference.ContextWithCallChain(ctx, instanceID)
	ctx = inference.ContextWithCallerID(ctx, instanceID)

	if inst.loop != nil {
		return inst.loop.Chat(ctx, message, files, onEvent)
	}
	return "", fmt.Errorf("instance %q has no inference loop", instanceID)
}

// SecretNames returns the names of available secrets from the control plane.
func (m *Manager) SecretNames() []string {
	if m.cp == nil {
		return nil
	}
	return m.cp.SecretNames()
}

// SecretEnv returns secret env vars from the control plane.
func (m *Manager) SecretEnv() []string {
	if m.cp == nil {
		return nil
	}
	return m.cp.SecretEnv()
}

// SetClusterService sets the cluster leader service for remote node management.
// Must be called before any remote spawns. If nil, all spawns are local.
func (m *Manager) SetClusterService(svc *cluster.LeaderService) {
	m.clusterService = svc
}

// ListNodes returns all nodes in the cluster registry. Returns nil if
// clustering is not enabled.
func (m *Manager) ListNodes() []ipc.NodeInfo {
	if m.clusterService == nil {
		return nil
	}
	nodes := m.clusterService.Registry().List()
	result := make([]ipc.NodeInfo, len(nodes))
	for i, n := range nodes {
		result[i] = ipc.NodeInfo{
			ID:          n.ID,
			Name:        n.Name,
			Status:      string(n.Status),
			IsHome:      n.IsHome,
			Capacity:    n.Capacity,
			ActiveCount: n.ActiveCount,
		}
	}
	return result
}

// extractAgentName extracts the agent name from a watcher path like "agents/foo/agent.md".
func extractAgentName(path string) string {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] != "agents" {
		return ""
	}
	return parts[1]
}

// Path helpers

func (m *Manager) agentDefDir(name string) string {
	return filepath.Join(m.rootDir, "agents", name)
}

func (m *Manager) sharedSkillsDir() string {
	return filepath.Join(m.rootDir, "skills")
}

func (m *Manager) instanceDir(id string) string {
	return filepath.Join(m.rootDir, "instances", id)
}

func (m *Manager) instanceSessionDir(instanceID, sessionID string) string {
	return filepath.Join(m.rootDir, "instances", instanceID, "sessions", sessionID)
}

// setInstanceStatus updates the instance status in the platform database.
func (m *Manager) setInstanceStatus(id, status string) {
	if m.pdb == nil {
		return
	}
	if err := m.pdb.UpdateInstanceStatus(id, status); err != nil {
		m.logger.Warn("failed to update instance status in db", "id", id, "status", status, "error", err)
	}
}

var validAgentName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validateAgentName rejects names that could escape the agents directory.
func validateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	if !validAgentName.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: only letters, numbers, hyphens, and underscores are allowed", name)
	}
	return nil
}
