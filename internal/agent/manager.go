package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/history"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/uidpool"
)

// AgentInfo describes a running agent for external consumers.
type AgentInfo struct {
	ID          string
	Name        string
	Mode        config.AgentMode
	Description string
	ParentID    string // empty for top-level agents
}

// WorkerHandle represents a running agent worker (process or mock).
type WorkerHandle struct {
	Worker ipc.AgentWorker
	Kill   func()          // force-kill the process (SIGKILL)
	Close  func()          // close gRPC conn, remove socket, etc.
	Done   <-chan struct{} // closed when the worker exits
}

// WorkerFactory creates agent workers. The default implementation spawns
// OS processes. Tests inject alternatives that return fake workers.
type WorkerFactory func(ctx context.Context, cfg ipc.SpawnConfig) (*WorkerHandle, error)

// runningAgent tracks a live agent session within the manager.
type runningAgent struct {
	mu             sync.Mutex // serializes calls through the worker
	info           AgentInfo
	worker         ipc.AgentWorker
	handle         *WorkerHandle
	effectiveTools map[string]bool // built-in tools this agent is allowed; nil = unrestricted
}

// Manager supervises agent lifecycles on a single node.
type Manager struct {
	mu       sync.RWMutex
	agents   map[string]*runningAgent // agent ID -> running agent
	children map[string][]string      // parent ID -> child IDs

	ctx          context.Context // long-lived context for persistent agents
	workspaceDir string
	opts         Options
	cp           ControlPlane // operator-level tool/secret config
	logger       *slog.Logger

	hostSocket    string        // path to host gRPC socket
	workerFactory WorkerFactory // creates agent workers (default = OS processes)
	uidPool       *uidpool.Pool // per-agent Unix user isolation; nil = disabled
}

// ControlPlane is the interface the Manager uses for operator-level config.
// Defined here to avoid a direct dependency on the controlplane package.
type ControlPlane interface {
	AgentTools(name string) (tools []string, ok bool)
	SecretNames() []string
	SecretEnv() []string
	ProviderInfo() (providerType string, apiKey string, ok bool)
	ProviderByType(providerType string) (apiKey string, ok bool)
	DefaultModel() string
}

// NewManager creates a new agent manager. workspaceDir is the root of the
// workspace containing agents/ and sessions/ subdirectories. The context
// controls the lifetime of persistent agents. cp may be nil if no control
// plane is configured. If wf is nil, the default OS process spawner is used.
func NewManager(ctx context.Context, workspaceDir string, opts Options, cp ControlPlane, logger *slog.Logger, hostSocket string, wf WorkerFactory, pool *uidpool.Pool) *Manager {
	if wf == nil {
		wf = defaultWorkerFactory
	}
	return &Manager{
		agents:        make(map[string]*runningAgent),
		children:      make(map[string][]string),
		ctx:           ctx,
		workspaceDir:  workspaceDir,
		opts:          opts,
		cp:            cp,
		logger:        logger,
		hostSocket:    hostSocket,
		workerFactory: wf,
		uidPool:       pool,
	}
}

// StartAgent loads an agent definition by name and starts it as a persistent
// agent supervised by the manager's lifetime context. The ctx parameter is used
// only for config loading and worker spawning, not for the running agent's lifetime.
// parentID tracks lineage; pass "" for top-level agents.
func (m *Manager) StartAgent(ctx context.Context, name, parentID string) (string, error) {
	if err := validateAgentName(name); err != nil {
		return "", err
	}

	cfg, err := config.LoadAgentDir(m.agentDefDir(name))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", name, err)
	}

	id := uuid.New().String()
	return m.startSession(ctx, id, cfg, parentID)
}

// SpawnSubagent starts an ephemeral agent that runs the given prompt and returns
// the result. Blocks until the subagent completes. The agent always runs in
// ephemeral mode regardless of its config file — the caller controls the lifecycle.
// parentID identifies the spawning agent (empty for top-level spawns).
// onDelta receives streaming deltas during execution (may be nil).
func (m *Manager) SpawnSubagent(ctx context.Context, agentName, prompt, parentID string, onDelta func(string) error) (string, error) {
	if err := validateAgentName(agentName); err != nil {
		return "", err
	}

	cfg, err := config.LoadAgentDir(m.agentDefDir(agentName))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", agentName, err)
	}
	// Always ephemeral — the caller controls the lifecycle, not the config.
	cfg.Mode = config.ModeEphemeral

	id := uuid.New().String()
	agentID, err := m.startSession(ctx, id, cfg, parentID)
	if err != nil {
		return "", err
	}

	// Run the prompt and collect the result
	result, err := m.SendMessage(ctx, agentID, prompt, onDelta)

	// Clean up the ephemeral agent and its entire subtree
	m.StopAgent(agentID)

	if err != nil {
		return "", fmt.Errorf("subagent %q failed: %w", agentName, err)
	}
	return result, nil
}

// SendMessage sends a message to a running agent and streams the response.
// onDelta is called for each token; it may be nil. Calls are serialized
// per agent to prevent conversation corruption.
func (m *Manager) SendMessage(ctx context.Context, agentID, message string, onDelta func(string) error) (string, error) {
	ra := m.getAgent(agentID)
	if ra == nil {
		return "", fmt.Errorf("agent %q not found", agentID)
	}

	ra.mu.Lock()
	defer ra.mu.Unlock()

	return ra.worker.Chat(ctx, message, onDelta)
}

// StopAgent stops a running agent and all its descendants.
// Returns the info of the stopped root agent.
func (m *Manager) StopAgent(agentID string) (ipc.AgentInfo, error) {
	// Collect the entire subtree in one snapshot, then stop leaf-first
	toStop := m.collectDescendants(agentID)
	if len(toStop) == 0 {
		return ipc.AgentInfo{}, fmt.Errorf("agent %q not found", agentID)
	}

	// Save root info before removal
	rootInfo, _ := m.GetAgent(agentID)

	for i := len(toStop) - 1; i >= 0; i-- {
		id := toStop[i]
		m.removeAgent(id)
		m.logger.Info("agent stopped", "id", id)
	}
	return ipc.AgentInfo{
		ID:          rootInfo.ID,
		Name:        rootInfo.Name,
		Mode:        string(rootInfo.Mode),
		Description: rootInfo.Description,
		ParentID:    rootInfo.ParentID,
	}, nil
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
func (m *Manager) ListChildren(callerID string) []ipc.AgentInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	childIDs := m.children[callerID]
	result := make([]ipc.AgentInfo, 0, len(childIDs))
	for _, cid := range childIDs {
		if ra, ok := m.agents[cid]; ok {
			result = append(result, ipc.AgentInfo{
				ID:          ra.info.ID,
				Name:        ra.info.Name,
				Mode:        string(ra.info.Mode),
				Description: ra.info.Description,
				ParentID:    ra.info.ParentID,
			})
		}
	}
	return result
}

// HistoryMessage is a simplified message for API consumers.
type HistoryMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// ErrAgentNotFound is returned when an agent ID does not match a running agent.
var ErrAgentNotFound = errors.New("agent not found")

// GetHistory returns recent messages from a persistent agent's conversation history.
// Opens the agent's history DB read-only, queries, and closes immediately to
// avoid blocking WAL checkpointing in the agent process.
func (m *Manager) GetHistory(agentID string, limit int) ([]HistoryMessage, error) {
	m.mu.RLock()
	ra, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrAgentNotFound
	}

	if !ra.info.Mode.IsPersistent() {
		return nil, nil
	}

	historyPath := filepath.Join(m.sessionDir(agentID), "history.db")
	store, err := history.OpenStoreReadOnly(historyPath)
	if err != nil {
		// Agent may not have created history.db yet (still starting).
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading history: %w", err)
	}
	defer store.Close()

	msgs, err := store.RecentMessages(limit)
	if err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}

	result := make([]HistoryMessage, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Role == "user" || msg.Role == "assistant" {
			result = append(result, HistoryMessage{
				Role:      msg.Role,
				Content:   msg.Content,
				Timestamp: msg.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return result, nil
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

// RestoreSessions scans the sessions/ directory and restarts any
// persistent agents that have manifests. Call once after NewManager.
func (m *Manager) RestoreSessions(ctx context.Context) error {
	dir := m.sessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("scanning sessions: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(dir, entry.Name(), "manifest.yaml")
		manifest, err := config.ReadManifest(manifestPath)
		if err != nil {
			m.logger.Warn("skipping session with unreadable manifest",
				"dir", entry.Name(), "error", err)
			continue
		}
		if !manifest.Mode.IsPersistent() {
			// Clean up stale ephemeral session dirs
			os.RemoveAll(filepath.Join(dir, entry.Name()))
			continue
		}

		// Validate manifest fields to prevent path traversal
		if err := validateAgentName(manifest.Agent); err != nil {
			m.logger.Warn("skipping session with invalid agent name",
				"dir", entry.Name(), "agent", manifest.Agent, "error", err)
			continue
		}
		if manifest.ID != entry.Name() {
			m.logger.Warn("skipping session where manifest ID does not match directory",
				"dir", entry.Name(), "manifest_id", manifest.ID)
			continue
		}

		cfg, err := config.LoadAgentDir(m.agentDefDir(manifest.Agent))
		if err != nil {
			m.logger.Warn("skipping session with missing agent definition",
				"agent", manifest.Agent, "error", err)
			continue
		}

		_, err = m.startSession(ctx, manifest.ID, cfg, manifest.ParentID)
		if err != nil {
			m.logger.Warn("failed to restore agent",
				"id", manifest.ID, "agent", manifest.Agent, "error", err)
		}
	}
	return nil
}

// Shutdown stops all running agents. Ephemeral session directories are cleaned up.
func (m *Manager) Shutdown() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.agents))
	for id := range m.agents {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	// Stop leaf-first by collecting full tree and reversing
	var ordered []string
	seen := make(map[string]bool)
	for _, id := range ids {
		descendants := m.collectDescendants(id)
		for _, d := range descendants {
			if !seen[d] {
				seen[d] = true
				ordered = append(ordered, d)
			}
		}
	}

	for i := len(ordered) - 1; i >= 0; i-- {
		m.removeAgent(ordered[i])
	}

	m.logger.Info("agent manager shut down")
}

// startSession creates a session directory, spawns a worker process,
// and registers the agent in the manager.
func (m *Manager) startSession(ctx context.Context, id string, cfg config.AgentConfig, parentID string) (string, error) {
	// Create session directory and write manifest
	sessDir := m.sessionDir(id)
	if err := os.MkdirAll(sessDir, 0700); err != nil {
		return "", fmt.Errorf("creating session dir: %w", err)
	}

	manifestPath := filepath.Join(sessDir, "manifest.yaml")
	_, statErr := os.Stat(manifestPath)
	switch {
	case os.IsNotExist(statErr):
		manifest := config.Manifest{
			ID:        id,
			Agent:     cfg.Name,
			Mode:      cfg.Mode,
			ParentID:  parentID,
			CreatedAt: time.Now(),
		}
		if err := config.WriteManifest(manifestPath, manifest); err != nil {
			return "", fmt.Errorf("writing manifest: %w", err)
		}
	case statErr != nil:
		return "", fmt.Errorf("checking manifest: %w", statErr)
	}

	// Compute effective tool set: declared tools ∩ control plane ∩ parent caps.
	effectiveTools := m.computeEffectiveTools(cfg, parentID)
	hasSkills := len(cfg.Skills) > 0
	if !hasSkills {
		skillsDir := filepath.Join(m.agentDefDir(cfg.Name), "skills")
		if _, err := os.Stat(skillsDir); err == nil {
			hasSkills = true
		}
	}
	allowedTools := buildAllowedToolsMap(effectiveTools, cfg.Mode, hasSkills)

	// Persistent agents use the manager's long-lived context so they
	// survive beyond the tool call that started them. Ephemeral agents
	// use the caller's context (typically the parent's tool call).
	spawnCtx := ctx
	if cfg.Mode.IsPersistent() {
		spawnCtx = m.ctx
	}

	// Acquire a dedicated Unix UID for this agent (if isolation is enabled).
	var uid, gid uint32
	var groups []uint32
	if m.uidPool != nil {
		var err error
		uid, gid, err = m.uidPool.Acquire(id)
		if err != nil {
			return "", fmt.Errorf("acquiring UID: %w", err)
		}
		groups = []uint32{gid}
		// Coordinator agents get write access to agents/ and skills/.
		if cfg.Mode == config.ModeCoordinator {
			if coordGID := m.uidPool.CoordinatorGID(); coordGID != 0 {
				groups = append(groups, coordGID)
			}
		}
		// Transfer ownership of the session dir to the agent user.
		// WalkDir handles both fresh dirs (just the dir itself) and
		// restored sessions (dir + existing files like history.db).
		if err := filepath.WalkDir(sessDir, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Chown(path, int(uid), int(gid))
		}); err != nil {
			// Chown requires root. If we're not root (misconfigured),
			// log and continue — the UID will still be set on the child
			// process, but file permissions won't isolate session dirs.
			if os.IsPermission(err) {
				m.logger.Warn("cannot chown session dir (not root); file isolation degraded",
					"session", id, "uid", uid)
			} else {
				m.uidPool.Release(id)
				return "", fmt.Errorf("chowning session dir: %w", err)
			}
		}
	}

	// Resolve provider from control plane (dynamic — picks up latest config).
	// When cp is nil (tests), provider/apiKey stay empty — the test worker
	// factory doesn't need them.
	var provider, apiKey string
	if m.cp != nil {
		if cfg.Provider != "" {
			// Agent specifies a provider override — look it up directly.
			var ok bool
			apiKey, ok = m.cp.ProviderByType(cfg.Provider)
			if !ok {
				return "", fmt.Errorf("agent %q requests provider %q which is not configured", cfg.Name, cfg.Provider)
			}
			provider = cfg.Provider
		} else {
			// Use the default provider.
			var ok bool
			provider, apiKey, ok = m.cp.ProviderInfo()
			if !ok {
				return "", fmt.Errorf("no LLM provider configured")
			}
		}
	}

	// Resolve model: agent config → control plane default → empty (provider default).
	model := cfg.Model
	if m.cp != nil {
		if dm := m.cp.DefaultModel(); dm != "" && model == "" {
			model = dm
		}
	}
	if m.opts.Model != "" {
		model = m.opts.Model
	}

	spawnCfg := ipc.SpawnConfig{
		SessionID:      id,
		AgentName:      cfg.Name,
		ParentID:       parentID,
		Mode:           string(cfg.Mode),
		EffectiveTools: allowedTools,
		WorkingDir:     m.opts.WorkingDir,
		SessionDir:     sessDir,
		AgentDefDir:    m.agentDefDir(cfg.Name),
		SharedSkillDir: m.sharedSkillsDir(),
		HostSocket:     m.hostSocket,
		Provider:       provider,
		APIKey:         apiKey,
		Model:          model,
		UID:            uid,
		GID:            gid,
		Groups:         groups,
	}

	handle, err := m.workerFactory(spawnCtx, spawnCfg)
	if err != nil {
		if m.uidPool != nil {
			m.uidPool.Release(id)
		}
		// Clean up session dir on failure (only if we just created it)
		if os.IsNotExist(statErr) {
			os.RemoveAll(sessDir)
		}
		return "", fmt.Errorf("spawning agent %q: %w", cfg.Name, err)
	}

	ra := &runningAgent{
		info: AgentInfo{
			ID:          id,
			Name:        cfg.Name,
			Mode:        cfg.Mode,
			Description: cfg.Description,
			ParentID:    parentID,
		},
		worker:         handle.Worker,
		handle:         handle,
		effectiveTools: effectiveTools,
	}

	m.mu.Lock()
	m.agents[id] = ra
	if parentID != "" {
		m.children[parentID] = append(m.children[parentID], id)
	}
	m.mu.Unlock()

	// Start death-watcher goroutine for unexpected process exits.
	go m.watchWorker(id, handle.Done)

	m.logger.Info("agent started",
		"id", id,
		"name", cfg.Name,
		"mode", cfg.Mode,
		"parent", parentID,
	)

	return id, nil
}

// watchWorker monitors a worker's Done channel and handles unexpected exits.
func (m *Manager) watchWorker(agentID string, done <-chan struct{}) {
	<-done

	m.mu.RLock()
	ra, ok := m.agents[agentID]
	m.mu.RUnlock()
	if !ok {
		return // already removed (normal shutdown)
	}

	m.logger.Warn("agent process exited unexpectedly",
		"id", agentID,
		"name", ra.info.Name,
	)

	// Unregister the dead agent and its children.
	descendants := m.collectDescendants(agentID)
	for i := len(descendants) - 1; i >= 0; i-- {
		id := descendants[i]
		m.mu.Lock()
		deadRA, exists := m.agents[id]
		m.unregisterLocked(id, deadRA)
		m.mu.Unlock()

		if exists {
			deadRA.handle.Close()
			if m.uidPool != nil {
				m.uidPool.Release(id)
			}
			if !deadRA.info.Mode.IsPersistent() {
				os.RemoveAll(m.sessionDir(id))
			}
		}
	}
}

// removeAgent gracefully shuts down and removes an agent from the registry.
// Ephemeral session directories are cleaned up.
func (m *Manager) removeAgent(id string) {
	m.mu.Lock()
	ra, ok := m.agents[id]
	m.unregisterLocked(id, ra)
	m.mu.Unlock()

	if !ok {
		return
	}

	// Graceful shutdown: ask the worker to stop, then wait for exit
	// under a single deadline.
	const shutdownGrace = 5 * time.Second
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	ra.worker.Shutdown(shutCtx)

	select {
	case <-ra.handle.Done:
		// Process exited cleanly.
	case <-shutCtx.Done():
		// Deadline expired — force-kill and wait.
		if ra.handle.Kill != nil {
			ra.handle.Kill()
		}
		<-ra.handle.Done
	}

	ra.handle.Close()

	if m.uidPool != nil {
		m.uidPool.Release(id)
	}

	if !ra.info.Mode.IsPersistent() {
		os.RemoveAll(m.sessionDir(id))
	}
}

// getAgent returns the runningAgent for the given ID, or nil.
func (m *Manager) getAgent(id string) *runningAgent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agents[id]
}

// unregisterLocked removes an agent from the registry and its parent's children
// list. Must be called with m.mu held.
func (m *Manager) unregisterLocked(id string, ra *runningAgent) {
	delete(m.agents, id)
	delete(m.children, id)
	if ra != nil && ra.info.ParentID != "" {
		siblings := m.children[ra.info.ParentID]
		updated := make([]string, 0, len(siblings))
		for _, cid := range siblings {
			if cid != id {
				updated = append(updated, cid)
			}
		}
		m.children[ra.info.ParentID] = updated
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

// spawnTool is injected into all agents — spawning ephemeral subagents is
// universally available since the subagent is fire-and-forget.
var spawnTool = "spawn_agent"

// coordinatorTools are injected only for coordinator-mode agents. These
// manage persistent agent lifecycles and require hive-coordinators group.
var coordinatorTools = []string{
	"start_agent", "list_agents", "send_message", "stop_agent",
}

// persistentTools are injected for persistent and coordinator agents.
var persistentTools = []string{
	"memory_read", "memory_write", "todos", "history_search", "history_recall",
}

// computeEffectiveTools returns the set of built-in tools this agent is
// allowed to use, computed as the intersection of:
//  1. The agent's declared tools (from agent.md frontmatter)
//  2. The control plane override (if any)
//  3. The parent's effective tools (if any)
//
// Returns nil if the agent has no restrictions (all tools allowed).
func (m *Manager) computeEffectiveTools(cfg config.AgentConfig, parentID string) map[string]bool {
	// Start with declared tools from agent.md.
	var effective map[string]bool
	if cfg.DeclaredTools != nil {
		effective = make(map[string]bool, len(cfg.DeclaredTools))
		for _, t := range cfg.DeclaredTools {
			effective[t] = true
		}
	}
	// No declared tools = closed by default (empty set, not nil).
	if effective == nil {
		effective = make(map[string]bool)
	}

	// Intersect with control plane override if present.
	if m.cp != nil {
		if cpTools, ok := m.cp.AgentTools(cfg.Name); ok {
			cpSet := make(map[string]bool, len(cpTools))
			for _, t := range cpTools {
				cpSet[t] = true
			}
			for t := range effective {
				if !cpSet[t] {
					delete(effective, t)
				}
			}
		}
	}

	// Intersect with parent's effective tools if parent exists.
	if parentID != "" {
		m.mu.RLock()
		parent, ok := m.agents[parentID]
		m.mu.RUnlock()
		if ok && parent.effectiveTools != nil {
			for t := range effective {
				if !parent.effectiveTools[t] {
					delete(effective, t)
				}
			}
		}
	}

	return effective
}

// buildAllowedToolsMap creates the AllowedTools map for agent.Options,
// adding mode-appropriate structural tools that bypass filtering.
func buildAllowedToolsMap(effective map[string]bool, mode config.AgentMode, hasSkills bool) map[string]bool {
	allowed := make(map[string]bool, len(effective)+10)
	for t := range effective {
		allowed[t] = true
	}

	// All agents can spawn ephemeral subagents.
	allowed[spawnTool] = true

	// Coordinator agents get full agent management tools.
	if mode == config.ModeCoordinator {
		for _, t := range coordinatorTools {
			allowed[t] = true
		}
	}

	// Persistent and coordinator agents get memory/todos/history tools.
	if mode.IsPersistent() {
		for _, t := range persistentTools {
			allowed[t] = true
		}
	}

	if hasSkills {
		allowed["use_skill"] = true
	}
	return allowed
}

// Path helpers

func (m *Manager) agentDefDir(name string) string {
	return filepath.Join(m.workspaceDir, "agents", name)
}

func (m *Manager) sharedSkillsDir() string {
	return filepath.Join(m.workspaceDir, "skills")
}

func (m *Manager) sessionsDir() string {
	return filepath.Join(m.workspaceDir, "sessions")
}

func (m *Manager) sessionDir(id string) string {
	return filepath.Join(m.workspaceDir, "sessions", id)
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
