package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/google/uuid"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/hub"
)

// AgentInfo describes a running agent for external consumers.
type AgentInfo struct {
	ID          string
	Name        string
	Mode        config.AgentMode
	Description string
	ParentID    string // empty for top-level agents
}

// runningAgent tracks a live agent instance within the manager.
type runningAgent struct {
	mu     sync.Mutex // serializes calls to this agent's conversation
	info   AgentInfo
	agent  *Agent
	conv   *Conversation
	cancel context.CancelFunc
}

// Manager supervises agent lifecycles on a single node.
type Manager struct {
	mu       sync.RWMutex
	agents   map[string]*runningAgent // agent ID -> running agent
	children map[string][]string      // parent ID -> child IDs

	ctx       context.Context // long-lived context for persistent agents
	agentsDir string
	swarm     *hub.Swarm
	opts      Options
	logger    *slog.Logger
}

// NewManager creates a new agent manager. The context controls the lifetime
// of persistent agents — cancel it to shut everything down.
func NewManager(ctx context.Context, agentsDir string, swarm *hub.Swarm, opts Options, logger *slog.Logger) *Manager {
	return &Manager{
		agents:    make(map[string]*runningAgent),
		children:  make(map[string][]string),
		ctx:       ctx,
		agentsDir: agentsDir,
		swarm:     swarm,
		opts:      opts,
		logger:    logger,
	}
}

// StartAgent loads an agent definition by name and starts it as a persistent
// agent supervised by the manager's lifetime context. The ctx parameter is used
// only for config loading and agent creation, not for the running agent's lifetime.
// parentID tracks lineage; pass "" for top-level agents.
func (m *Manager) StartAgent(ctx context.Context, name, parentID string) (string, error) {
	if err := validateAgentName(name); err != nil {
		return "", err
	}

	cfg, err := config.LoadAgentDir(m.agentDir(name))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", name, err)
	}

	return m.startFromConfig(ctx, cfg, parentID)
}

// SpawnSubagent starts an ephemeral agent that runs the given prompt and returns
// the result. Blocks until the subagent completes. The agent always runs in
// ephemeral mode regardless of its config file — the caller controls the lifecycle.
// parentID identifies the spawning agent (empty for top-level spawns).
func (m *Manager) SpawnSubagent(ctx context.Context, agentName, prompt, parentID string) (string, error) {
	if err := validateAgentName(agentName); err != nil {
		return "", err
	}

	cfg, err := config.LoadAgentDir(m.agentDir(agentName))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", agentName, err)
	}
	// Always ephemeral — the caller controls the lifecycle, not the config.
	cfg.Mode = config.ModeEphemeral

	id, err := m.startFromConfig(ctx, cfg, parentID)
	if err != nil {
		return "", err
	}

	// Run the prompt and collect the result
	result, err := m.SendMessage(ctx, id, prompt, nil)

	// Clean up the ephemeral agent and its entire subtree
	m.StopAgent(id)

	if err != nil {
		return "", fmt.Errorf("subagent %q failed: %w", agentName, err)
	}
	return result, nil
}

// SendMessage sends a message to a running agent and streams the response.
// onDelta is called for each token; it may be nil. Calls are serialized
// per agent to prevent conversation corruption.
//
// NOTE: Tool calls executed during StreamChat run within the ra.mu lock.
// This means a tool handler must not synchronously call SendMessage back
// to the same agent, or it will deadlock. Use SpawnSubagent or StreamChat
// for patterns that need bidirectional communication.
func (m *Manager) SendMessage(ctx context.Context, agentID, message string, onDelta func(string) error) (string, error) {
	m.mu.RLock()
	ra, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("agent %q not found", agentID)
	}

	ra.mu.Lock()
	defer ra.mu.Unlock()

	return ra.agent.StreamChat(ctx, ra.conv, message, onDelta)
}

// StreamChat gives the caller direct access to an agent's StreamChat with
// a caller-owned conversation. Use this when each caller needs isolated
// conversation history (e.g. per-WebSocket-connection chat).
func (m *Manager) StreamChat(ctx context.Context, agentID string, conv *Conversation, message string, onDelta func(string) error) (string, error) {
	m.mu.RLock()
	ra, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("agent %q not found", agentID)
	}

	return ra.agent.StreamChat(ctx, conv, message, onDelta)
}

// StopAgent stops a running agent and all its descendants.
// Returns the info of the stopped root agent.
func (m *Manager) StopAgent(agentID string) (AgentInfo, error) {
	// Collect the entire subtree in one snapshot, then stop leaf-first
	toStop := m.collectDescendants(agentID)
	if len(toStop) == 0 {
		return AgentInfo{}, fmt.Errorf("agent %q not found", agentID)
	}

	// Save root info before removal
	rootInfo, _ := m.GetAgent(agentID)

	for i := len(toStop) - 1; i >= 0; i-- {
		id := toStop[i]
		m.removeAgent(id)
		m.logger.Info("agent stopped", "id", id)
	}
	return rootInfo, nil
}

// GetAgent returns info about a running agent.
func (m *Manager) GetAgent(agentID string) (AgentInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ra, ok := m.agents[agentID]
	if !ok {
		return AgentInfo{}, false
	}
	return ra.info, true
}

// ListAgents returns a snapshot of all running agents.
func (m *Manager) ListAgents() []AgentInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]AgentInfo, 0, len(m.agents))
	for _, ra := range m.agents {
		result = append(result, ra.info)
	}
	return result
}

// ListChildren returns a snapshot of agents that are direct children of callerID.
func (m *Manager) ListChildren(callerID string) []AgentInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	childIDs := m.children[callerID]
	result := make([]AgentInfo, 0, len(childIDs))
	for _, cid := range childIDs {
		if ra, ok := m.agents[cid]; ok {
			result = append(result, ra.info)
		}
	}
	return result
}

// AgentByName returns the ID of a running agent by name.
// If multiple agents share a name, the result is nondeterministic.
func (m *Manager) AgentByName(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, ra := range m.agents {
		if ra.info.Name == name {
			return id, true
		}
	}
	return "", false
}

// IsDescendant reports whether targetID is a descendant of (or equal to) ancestorID.
func (m *Manager) IsDescendant(targetID, ancestorID string) bool {
	descendants := m.collectDescendants(ancestorID)
	for _, id := range descendants {
		if id == targetID {
			return true
		}
	}
	return false
}

// Shutdown stops all running agents.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ra := range m.agents {
		ra.cancel()
	}
	m.agents = make(map[string]*runningAgent)
	m.children = make(map[string][]string)
	m.logger.Info("agent manager shut down")
}

// startFromConfig creates and registers an agent from a loaded config.
func (m *Manager) startFromConfig(ctx context.Context, cfg config.AgentConfig, parentID string) (string, error) {
	id := uuid.New().String()

	// Persistent agents use the manager's long-lived context so they
	// survive beyond the tool call that started them. Ephemeral agents
	// use the caller's context (typically the parent's tool call).
	baseCtx := ctx
	if cfg.Mode == config.ModePersistent {
		baseCtx = m.ctx
	}
	agentCtx, cancel := context.WithCancel(baseCtx)

	// Build options with manager tools injected
	opts := m.opts
	opts.ExtraTools = m.buildManagerTools(id)

	a, err := New(agentCtx, cfg, m.swarm, opts, m.logger)
	if err != nil {
		cancel()
		return "", fmt.Errorf("creating agent %q: %w", cfg.Name, err)
	}

	ra := &runningAgent{
		info: AgentInfo{
			ID:          id,
			Name:        cfg.Name,
			Mode:        cfg.Mode,
			Description: cfg.Description,
			ParentID:    parentID,
		},
		agent:  a,
		conv:   NewConversation(),
		cancel: cancel,
	}

	m.mu.Lock()
	m.agents[id] = ra
	if parentID != "" {
		m.children[parentID] = append(m.children[parentID], id)
	}
	m.mu.Unlock()

	m.logger.Info("agent started",
		"id", id,
		"name", cfg.Name,
		"mode", cfg.Mode,
		"parent", parentID,
	)

	return id, nil
}

// removeAgent cancels and removes an agent from the registry.
func (m *Manager) removeAgent(id string) {
	m.mu.Lock()
	ra, ok := m.agents[id]
	delete(m.agents, id)
	delete(m.children, id)
	// Remove from parent's children list (build a new slice to avoid
	// mutating the backing array that other readers may reference).
	if ok && ra.info.ParentID != "" {
		siblings := m.children[ra.info.ParentID]
		updated := make([]string, 0, len(siblings))
		for _, cid := range siblings {
			if cid != id {
				updated = append(updated, cid)
			}
		}
		m.children[ra.info.ParentID] = updated
	}
	m.mu.Unlock()
	if ok {
		ra.cancel()
	}
}

// collectDescendants returns agentID plus all its descendants via BFS,
// in parent-first order (reverse for leaf-first shutdown).
func (m *Manager) collectDescendants(agentID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.agents[agentID]; !ok {
		return nil
	}

	var result []string
	queue := []string{agentID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		result = append(result, current)
		queue = append(queue, m.children[current]...)
	}
	return result
}

func (m *Manager) agentDir(name string) string {
	return filepath.Join(m.agentsDir, name)
}

var validAgentName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validateAgentName rejects names that could escape the agents directory.
// Only letters, numbers, hyphens, and underscores are allowed.
func validateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	if !validAgentName.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: only letters, numbers, hyphens, and underscores are allowed", name)
	}
	return nil
}
