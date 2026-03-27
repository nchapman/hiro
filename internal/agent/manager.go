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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nchapman/hivebot/internal/config"
	"charm.land/fantasy"

	"github.com/nchapman/hivebot/internal/inference"
	"github.com/nchapman/hivebot/internal/ipc"
	"github.com/nchapman/hivebot/internal/models"
	"github.com/nchapman/hivebot/internal/ipc/grpcipc"
	platformdb "github.com/nchapman/hivebot/internal/platform/db"
	"github.com/nchapman/hivebot/internal/uidpool"
	"github.com/nchapman/hivebot/internal/watcher"
)

// SessionStatus represents the lifecycle state of an agent.
type SessionStatus string

const (
	SessionStatusRunning SessionStatus = "running"
	SessionStatusStopped SessionStatus = "stopped"
)

// SessionInfo describes an agent for external consumers.
type SessionInfo struct {
	ID          string
	Name        string
	Mode        config.AgentMode
	Description string
	ParentID    string // empty for top-level agents
	Status      SessionStatus
	Model       string // resolved model ID
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

// session tracks a live agent session within the manager.
type session struct {
	mu             sync.Mutex // serializes calls through the worker
	info           SessionInfo
	worker         ipc.AgentWorker
	handle         *WorkerHandle
	loop           *inference.Loop // inference loop (runs in control plane)
	effectiveTools map[string]bool // built-in tools this agent is allowed; nil = unrestricted
}

// Manager supervises agent lifecycles on a single node.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*session // agent ID -> running agent
	children map[string][]string      // parent ID -> child IDs

	ctx     context.Context // long-lived context for persistent agents
	rootDir string
	opts    Options
	cp      ControlPlane // operator-level tool/secret config
	logger  *slog.Logger

	workerFactory WorkerFactory // creates agent workers (default = OS processes)
	uidPool       *uidpool.Pool // per-agent Unix user isolation; nil = disabled
	pdb           *platformdb.DB // unified platform database
}

// ControlPlane is the interface the Manager uses for operator-level config.
// Defined here to avoid a direct dependency on the controlplane package.
type ControlPlane interface {
	AgentTools(name string) (tools []string, ok bool)
	SecretNames() []string
	SecretEnv() []string
	ProviderInfo() (providerType string, apiKey string, baseURL string, ok bool)
	ProviderByType(providerType string) (apiKey string, baseURL string, ok bool)
	ConfiguredProviderTypes() []string
	DefaultModel() string
}

// NewManager creates a new agent manager. rootDir is the hive platform root
// containing agents/, sessions/, skills/, and workspace/ subdirectories. The context
// controls the lifetime of persistent agents. cp may be nil if no control
// plane is configured. If wf is nil, the default OS process spawner is used.
func NewManager(ctx context.Context, rootDir string, opts Options, cp ControlPlane, logger *slog.Logger, wf WorkerFactory, pool *uidpool.Pool, pdb *platformdb.DB) *Manager {
	if wf == nil {
		wf = defaultWorkerFactory
	}
	return &Manager{
		sessions:      make(map[string]*session),
		children:      make(map[string][]string),
		ctx:           ctx,
		rootDir:  rootDir,
		opts:          opts,
		cp:            cp,
		logger:        logger,
		workerFactory: wf,
		uidPool:       pool,
		pdb:           pdb,
	}
}

// CreateSession loads an agent definition by name and starts a session in the
// given mode. The ctx parameter is used only for config loading and worker
// spawning — persistent/coordinator sessions use the manager's lifetime context.
// parentID tracks lineage; pass "" for top-level agents.
// mode is a string to satisfy the ipc.HostManager interface boundary; it must
// be one of "persistent", "ephemeral", or "coordinator".
func (m *Manager) CreateSession(ctx context.Context, name, parentID string, mode string) (string, error) {
	if err := validateAgentName(name); err != nil {
		return "", err
	}

	agentMode := config.AgentMode(mode)
	switch agentMode {
	case config.ModePersistent, config.ModeEphemeral, config.ModeCoordinator:
		// valid
	default:
		return "", fmt.Errorf("invalid mode %q: must be persistent, ephemeral, or coordinator", mode)
	}

	cfg, err := config.LoadAgentDir(m.agentDefDir(name))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", name, err)
	}

	id := uuid.Must(uuid.NewV7()).String()
	return m.startSession(ctx, id, cfg, parentID, agentMode)
}

// SpawnSession starts an ephemeral agent that runs the given prompt and returns
// the result. Blocks until the subagent completes. The agent always runs in
// ephemeral mode — the caller controls the lifecycle.
// parentID identifies the spawning agent (empty for top-level spawns).
// onEvent receives streaming events during execution (may be nil).
func (m *Manager) SpawnSession(ctx context.Context, agentName, prompt, parentID string, onEvent func(ipc.ChatEvent) error) (string, error) {
	if err := validateAgentName(agentName); err != nil {
		return "", err
	}

	cfg, err := config.LoadAgentDir(m.agentDefDir(agentName))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", agentName, err)
	}

	id := uuid.Must(uuid.NewV7()).String()
	agentID, err := m.startSession(ctx, id, cfg, parentID, config.ModeEphemeral)
	if err != nil {
		return "", err
	}

	// Run the prompt and collect the result
	result, err := m.SendMessage(ctx, agentID, prompt, onEvent)

	// Clean up the ephemeral agent and its entire subtree
	m.StopSession(agentID)

	if err != nil {
		return "", fmt.Errorf("subagent %q failed: %w", agentName, err)
	}
	return result, nil
}

// UpdateSessionConfig changes the model and/or reasoning effort for a running session.
// Changes take effect on the next Chat() call.
func (m *Manager) UpdateSessionConfig(ctx context.Context, sessionID, model string, reasoningEffort *string) error {
	ra := m.getSession(sessionID)
	if ra == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}
	if ra.info.Status == SessionStatusStopped {
		return fmt.Errorf("session %q is stopped", sessionID)
	}
	if ra.loop == nil {
		return fmt.Errorf("session %q has no inference loop", sessionID)
	}

	// Serialize with SendMessage to prevent concurrent access to session state.
	ra.mu.Lock()
	defer ra.mu.Unlock()

	if model != "" && model != ra.info.Model {
		// Find which configured provider owns this model.
		provider, apiKey, baseURL, err := m.resolveProviderForModel(model)
		if err != nil {
			return err
		}

		lm, err := CreateLanguageModel(ctx, ProviderType(provider), apiKey, baseURL, model)
		if err != nil {
			return fmt.Errorf("creating language model %q: %w", model, err)
		}
		ra.loop.UpdateModel(lm, model, provider)
		ra.info.Model = model
	}

	if reasoningEffort != nil {
		if !validReasoningEffort(*reasoningEffort) {
			return fmt.Errorf("invalid reasoning effort %q", *reasoningEffort)
		}
		ra.loop.SetReasoningEffort(*reasoningEffort)
	}

	// Persist config to DB so it survives restarts.
	if m.pdb != nil {
		cfg := platformdb.SessionConfig{
			ModelOverride:   ra.info.Model,
			ReasoningEffort: ra.loop.ReasoningEffort(),
		}
		if err := m.pdb.UpdateSessionConfig(sessionID, cfg); err != nil {
			m.logger.Warn("failed to persist session config", "session", sessionID, "error", err)
		}
	}

	return nil
}

var validEfforts = map[string]bool{
	"": true, "on": true, "low": true, "medium": true, "high": true, "max": true,
	"minimal": true, "xhigh": true, // OpenAI/OpenRouter levels
}

func validReasoningEffort(effort string) bool {
	return validEfforts[effort]
}

// SendMessage sends a message to a running agent and streams the response.
// onEvent is called for each streaming event; it may be nil. Calls are serialized
// per agent to prevent conversation corruption.
func (m *Manager) SendMessage(ctx context.Context, agentID, message string, onEvent func(ipc.ChatEvent) error) (string, error) {
	return m.SendMessageWithFiles(ctx, agentID, message, nil, onEvent)
}

// SendMessageWithFiles is like SendMessage but includes file attachments
// (images, PDFs, text documents) passed to the inference loop as fantasy.FileParts.
func (m *Manager) SendMessageWithFiles(ctx context.Context, agentID, message string, files []fantasy.FilePart, onEvent func(ipc.ChatEvent) error) (string, error) {
	// Cycle detection: prevent re-entrant deadlocks when coordinator tools
	// send messages in a loop (A → send_message(B) → B sends back to A).
	if inference.IsInCallChain(ctx, agentID) {
		return "", fmt.Errorf("circular message dependency: session %s is already awaiting a response in this call chain", agentID)
	}

	ra := m.getSession(agentID)
	if ra == nil {
		return "", fmt.Errorf("session %q not found", agentID)
	}
	if ra.info.Status == SessionStatusStopped {
		return "", fmt.Errorf("session %q is stopped", agentID)
	}

	ra.mu.Lock()
	defer ra.mu.Unlock()

	// Add this session to the call chain and set the caller ID for tool scoping.
	ctx = inference.ContextWithCallChain(ctx, agentID)
	ctx = inference.ContextWithCallerID(ctx, agentID)

	if ra.loop != nil {
		return ra.loop.Chat(ctx, message, files, onEvent)
	}
	return "", fmt.Errorf("session %q has no inference loop", agentID)
}

// StopAgent stops a running agent and all its descendants.
// Persistent agents are soft-stopped (process killed, kept in registry as "stopped").
// Ephemeral agents are fully removed. Returns the info of the stopped root agent.
func (m *Manager) StopSession(agentID string) (ipc.SessionInfo, error) {
	// Collect the entire subtree in one snapshot, then stop leaf-first
	toStop := m.collectDescendants(agentID)
	if len(toStop) == 0 {
		return ipc.SessionInfo{}, fmt.Errorf("session %q not found", agentID)
	}

	// Check if already stopped
	rootInfo, _ := m.GetSession(agentID)
	if rootInfo.Status == SessionStatusStopped {
		return m.sessionInfoToIPC(rootInfo), nil
	}

	for i := len(toStop) - 1; i >= 0; i-- {
		id := toStop[i]
		ra := m.getSession(id)
		if ra == nil || ra.info.Status == SessionStatusStopped {
			continue
		}
		if ra.info.Mode.IsPersistent() {
			m.softStop(id)
		} else {
			m.removeSession(id)
		}
		m.logger.Info("session stopped", "id", id)
	}

	// Re-read info after stop (status may have changed)
	rootInfo, _ = m.GetSession(agentID)
	return m.sessionInfoToIPC(rootInfo), nil
}

// DeleteAgent stops and permanently removes an agent and all its descendants.
// Session directories are always deleted regardless of agent mode.
func (m *Manager) DeleteSession(agentID string) error {
	toStop := m.collectDescendants(agentID)
	if len(toStop) == 0 {
		return fmt.Errorf("session %q not found", agentID)
	}

	for i := len(toStop) - 1; i >= 0; i-- {
		id := toStop[i]
		m.mu.RLock()
		ra, ok := m.sessions[id]
		m.mu.RUnlock()
		if !ok {
			continue
		}

		if ra.info.Status == SessionStatusStopped {
			// Already stopped — just unregister and delete session dir.
			m.mu.Lock()
			m.unregisterLocked(id, ra)
			m.mu.Unlock()
		} else {
			// Running — do full graceful shutdown + unregister.
			m.removeSession(id)
		}

		// Always delete session dir and DB record regardless of mode.
		os.RemoveAll(m.sessionDir(id))
		if m.pdb != nil {
			m.pdb.DeleteSession(id)
		}
		m.logger.Info("session deleted", "id", id)
	}
	return nil
}

// RestartAgent restarts a stopped persistent agent, reusing its session directory.
func (m *Manager) StartSession(ctx context.Context, agentID string) error {
	m.mu.RLock()
	ra, ok := m.sessions[agentID]
	m.mu.RUnlock()
	if !ok {
		return ErrSessionNotFound
	}
	if ra.info.Status != SessionStatusStopped {
		return ErrSessionNotStopped
	}

	name := ra.info.Name
	parentID := ra.info.ParentID
	mode := ra.info.Mode

	// Remove the stopped entry so startSession can re-register it.
	m.mu.Lock()
	m.unregisterLocked(agentID, ra)
	m.mu.Unlock()

	cfg, err := config.LoadAgentDir(m.agentDefDir(name))
	if err != nil {
		// Re-register as stopped so the session remains visible.
		m.reregisterStopped(agentID, ra)
		return fmt.Errorf("loading agent %q: %w", name, err)
	}

	if _, err = m.startSession(ctx, agentID, cfg, parentID, mode); err != nil {
		// Re-register as stopped so the session remains visible.
		m.reregisterStopped(agentID, ra)
		return err
	}

	// Clear the stopped flag so the agent starts on next server restart.
	m.setSessionStatus(agentID, "running")
	return nil
}

// shutdownWorker sends a graceful shutdown to the worker, waits for exit under
// a deadline, and force-kills if necessary. Does not modify the session registry.
const shutdownGrace = 5 * time.Second

func (m *Manager) shutdownWorker(ra *session) {
	if ra.worker == nil {
		return
	}
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
}

// cleanupWorker closes the worker handle and releases the UID.
// The handle is captured under the lock to avoid races.
func (m *Manager) cleanupWorker(id string, h *WorkerHandle) {
	if h != nil {
		h.Close()
	}
	if m.uidPool != nil {
		m.uidPool.Release(id)
	}
}

// softStop gracefully shuts down a persistent agent's worker process
// but keeps it in the registry with status "stopped".
func (m *Manager) softStop(id string) {
	m.mu.RLock()
	ra, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return
	}

	m.shutdownWorker(ra)

	// Mark stopped BEFORE cleanup so watchWorker sees it and bails out.
	m.mu.Lock()
	h := ra.handle
	ra.worker = nil
	ra.handle = nil
	ra.loop = nil
	ra.info.Status = SessionStatusStopped
	m.mu.Unlock()

	m.cleanupWorker(id, h)

	// Persist stopped state so it survives server restarts.
	m.setSessionStatus(id, "stopped")
}

// reregisterStopped puts a session back into the registry as stopped.
// Used when StartSession fails after unregistering.
func (m *Manager) reregisterStopped(id string, s *session) {
	s.info.Status = SessionStatusStopped
	m.mu.Lock()
	m.sessions[id] = s
	if s.info.ParentID != "" {
		m.children[s.info.ParentID] = append(m.children[s.info.ParentID], id)
	}
	m.mu.Unlock()
}

// sessionInfoToIPC converts an SessionInfo to ipc.SessionInfo.
func (m *Manager) sessionInfoToIPC(info SessionInfo) ipc.SessionInfo {
	return ipc.SessionInfo{
		ID:          info.ID,
		Name:        info.Name,
		Mode:        string(info.Mode),
		Description: info.Description,
		ParentID:    info.ParentID,
		Status:      string(info.Status),
		Model:       info.Model,
	}
}

// GetAgent returns info about an agent (running or stopped).
func (m *Manager) GetSession(agentID string) (SessionInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ra, ok := m.sessions[agentID]
	if !ok {
		return SessionInfo{}, false
	}
	return ra.info, true
}

// ListAgents returns a snapshot of all agents (running and stopped) sorted by creation order.
// Agent IDs are UUIDv7 (time-ordered), so lexicographic sort gives creation order.
func (m *Manager) ListSessions() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]SessionInfo, 0, len(m.sessions))
	for _, ra := range m.sessions {
		result = append(result, ra.info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// ListChildren returns a snapshot of agents that are direct children of callerID.
func (m *Manager) ListChildSessions(callerID string) []ipc.SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	childIDs := m.children[callerID]
	result := make([]ipc.SessionInfo, 0, len(childIDs))
	for _, cid := range childIDs {
		if ra, ok := m.sessions[cid]; ok {
			result = append(result, ipc.SessionInfo{
				ID:          ra.info.ID,
				Name:        ra.info.Name,
				Mode:        string(ra.info.Mode),
				Description: ra.info.Description,
				ParentID:    ra.info.ParentID,
				Status:      string(ra.info.Status),
				Model:       ra.info.Model,
			})
		}
	}
	return result
}

// HistoryMessage is a simplified message for API consumers.
type HistoryMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	RawJSON   string `json:"raw_json,omitempty"` // full fantasy.Message JSON with tool calls
	Timestamp string `json:"timestamp"`
}

// ErrSessionNotFound is returned when an agent ID does not match a known agent.
var ErrSessionNotFound = errors.New("session not found")

// ErrSessionNotStopped is returned when an operation requires a stopped agent.
var ErrSessionNotStopped = errors.New("session is not stopped")

// GetHistory returns recent messages from a persistent agent's conversation history.
func (m *Manager) GetHistory(agentID string, limit int) ([]HistoryMessage, error) {
	m.mu.RLock()
	ra, ok := m.sessions[agentID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrSessionNotFound
	}

	if !ra.info.Mode.IsPersistent() {
		return nil, nil
	}

	if m.pdb == nil {
		return nil, nil
	}

	msgs, err := m.pdb.RecentMessages(agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}

	result := make([]HistoryMessage, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Role == "user" || msg.Role == "assistant" || msg.Role == "tool" {
			rawJSON := msg.RawJSON
			if msg.Role == "assistant" && rawJSON != "" {
				rawJSON = inference.InjectStatusMessages(rawJSON)
			}
			result = append(result, HistoryMessage{
				Role:      msg.Role,
				Content:   msg.Content,
				RawJSON:   rawJSON,
				Timestamp: msg.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return result, nil
}

// SessionByAgentName returns the ID and status of an agent by name.
// Prefers running sessions; falls back to stopped ones. If multiple
// sessions share a name, running ones take priority.
func (m *Manager) SessionByAgentName(name string) (id string, running bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for sid, ra := range m.sessions {
		if ra.info.Name == name {
			if ra.info.Status == SessionStatusRunning {
				return sid, true
			}
			// Remember the stopped one, but keep looking for a running one.
			id = sid
		}
	}
	return id, false
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

// RestoreSessions reads persistent/coordinator sessions from the platform
// database and restarts them. Call once after NewManager.
func (m *Manager) RestoreSessions(ctx context.Context) error {
	if m.pdb == nil {
		return nil
	}

	sessions, err := m.pdb.ListSessions("", "")
	if err != nil {
		return fmt.Errorf("listing sessions from db: %w", err)
	}

	for _, s := range sessions {
		mode := config.AgentMode(s.Mode)
		if !mode.IsPersistent() {
			// Clean up stale ephemeral sessions from db and disk.
			m.pdb.DeleteSession(s.ID)
			os.RemoveAll(m.sessionDir(s.ID))
			continue
		}

		if err := validateAgentName(s.AgentName); err != nil {
			m.logger.Warn("skipping session with invalid agent name",
				"id", s.ID, "agent", s.AgentName, "error", err)
			continue
		}

		cfg, err := config.LoadAgentDir(m.agentDefDir(s.AgentName))
		if err != nil {
			m.logger.Warn("skipping session with missing agent definition",
				"agent", s.AgentName, "error", err)
			continue
		}

		if s.Status == "stopped" {
			ra := &session{
				info: SessionInfo{
					ID:          s.ID,
					Name:        cfg.Name,
					Mode:        mode,
					Description: cfg.Description,
					ParentID:    s.ParentID,
					Status:      SessionStatusStopped,
					Model:       m.resolveModel(cfg),
				},
			}
			m.mu.Lock()
			m.sessions[s.ID] = ra
			if s.ParentID != "" {
				m.children[s.ParentID] = append(m.children[s.ParentID], s.ID)
			}
			m.mu.Unlock()
			m.logger.Info("restored stopped agent",
				"id", s.ID, "name", cfg.Name)
			continue
		}

		// Verify session dir exists — without it, history and state are lost.
		if _, err := os.Stat(m.sessionDir(s.ID)); os.IsNotExist(err) {
			m.logger.Warn("session dir missing, removing orphaned DB record",
				"id", s.ID, "agent", s.AgentName)
			m.pdb.DeleteSession(s.ID)
			continue
		}

		_, err = m.startSession(ctx, s.ID, cfg, s.ParentID, mode)
		if err != nil {
			m.logger.Warn("failed to restore agent",
				"id", s.ID, "agent", s.AgentName, "error", err)
			continue
		}

		// Restore per-session config (model override, reasoning effort).
		if sessCfg, cfgErr := m.pdb.GetSessionConfig(s.ID); cfgErr == nil {
			if sessCfg.ModelOverride != "" || sessCfg.ReasoningEffort != "" {
				effort := sessCfg.ReasoningEffort
				_ = m.UpdateSessionConfig(ctx, s.ID, sessCfg.ModelOverride, &effort)
			}
		}
	}
	return nil
}

// Shutdown stops all running agents. Ephemeral session directories are cleaned up.
// Stopped agents are unregistered without attempting worker shutdown.
func (m *Manager) Shutdown() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
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
		id := ordered[i]
		ra := m.getSession(id)
		if ra != nil && ra.info.Status == SessionStatusStopped {
			// Already stopped — just unregister.
			m.mu.Lock()
			m.unregisterLocked(id, ra)
			m.mu.Unlock()
			continue
		}
		m.removeSession(id)
	}

	m.logger.Info("session manager shut down")
}

// startSession creates a session directory, spawns a worker process,
// and registers the agent in the manager.
func (m *Manager) startSession(ctx context.Context, id string, cfg config.AgentConfig, parentID string, mode config.AgentMode) (string, error) {
	// Create session directory and standard subdirectories.
	sessDir := m.sessionDir(id)
	_, statErr := os.Stat(sessDir)
	dirIsNew := os.IsNotExist(statErr)
	if err := os.MkdirAll(sessDir, 0700); err != nil {
		return "", fmt.Errorf("creating session dir: %w", err)
	}
	for _, sub := range []string{"db", "scratch", "tmp"} {
		if err := os.MkdirAll(filepath.Join(sessDir, sub), 0700); err != nil {
			return "", fmt.Errorf("creating session %s dir: %w", sub, err)
		}
	}

	// Register session in the platform database.
	if m.pdb != nil {
		if err := m.pdb.CreateSession(platformdb.Session{
			ID:        id,
			AgentName: cfg.Name,
			Mode:      string(mode),
			ParentID:  parentID,
		}); err != nil && !errors.Is(err, platformdb.ErrDuplicate) {
			// ErrDuplicate is expected on the restore path — session already exists.
			return "", fmt.Errorf("creating session in db: %w", err)
		}
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
	allowedTools := buildAllowedToolsMap(effectiveTools, mode, hasSkills)

	// Persistent agents use the manager's long-lived context so they
	// survive beyond the tool call that started them. Ephemeral agents
	// use the caller's context (typically the parent's tool call).
	spawnCtx := ctx
	if mode.IsPersistent() {
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
		if mode == config.ModeCoordinator {
			if coordGID := m.uidPool.CoordinatorGID(); coordGID != 0 {
				groups = append(groups, coordGID)
			}
		}
		// Transfer ownership of the session dir to the agent user.
		// WalkDir handles both fresh dirs (just the dir itself) and
		// restored sessions (dir + existing files like memory.md, todos.yaml).
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

	// Resolve provider and model from control plane config.
	provider, apiKey, baseURL, err := m.resolveProvider(cfg)
	if err != nil {
		return "", err
	}
	model := m.resolveModel(cfg)
	cfg.Model = model // ensure the loop sees the resolved model

	spawnCfg := ipc.SpawnConfig{
		SessionID:      id,
		AgentName:      cfg.Name,
		EffectiveTools: allowedTools,
		WorkingDir:     m.opts.WorkingDir,
		SessionDir:     sessDir,
		AgentSocket:    filepath.Join(os.TempDir(), fmt.Sprintf("hive-agent-%s.sock", id)),
		UID:            uid,
		GID:            gid,
		Groups:         groups,
	}

	handle, err := m.workerFactory(spawnCtx, spawnCfg)
	if err == nil {
		// Inject secret env provider so bash commands in the worker can access secrets.
		if wc, ok := handle.Worker.(*grpcipc.WorkerClient); ok {
			wc.SetSecretEnvFn(m.SecretEnv)
		}
	}
	if err != nil {
		if m.uidPool != nil {
			m.uidPool.Release(id)
		}
		// Clean up session dir on failure (only if we just created it)
		if dirIsNew {
			os.RemoveAll(sessDir)
		}
		return "", fmt.Errorf("spawning agent %q: %w", cfg.Name, err)
	}

	// Create the inference loop (skipped if no provider — test mode).
	var loop *inference.Loop
	if provider != "" {
		lm, err := CreateLanguageModel(spawnCtx, ProviderType(provider), apiKey, baseURL, model)
		if err != nil {
			handle.Kill()
			if m.uidPool != nil {
				m.uidPool.Release(id)
			}
			if dirIsNew {
				os.RemoveAll(sessDir)
			}
			return "", fmt.Errorf("creating language model for %q: %w", cfg.Name, err)
		}

		loop, err = inference.NewLoop(inference.LoopConfig{
			SessionID:      id,
			AgentConfig:    cfg,
			Mode:           mode,
			WorkingDir:     m.opts.WorkingDir,
			SessionDir:     sessDir,
			AgentDefDir:    m.agentDefDir(cfg.Name),
			SharedSkillDir: m.sharedSkillsDir(),
			LM:             lm,
			Executor:       handle.Worker,
			PDB:            m.pdb,
			AllowedTools:   allowedTools,
			HasSkills:      hasSkills,
			SecretNamesFn:  m.SecretNames,
			SecretEnvFn:    m.SecretEnv,
			Logger:         m.logger.With("session", id, "agent", cfg.Name),
			HostManager:    m,
			CallerMode:     mode,
		})
		if err != nil {
			handle.Kill()
			if m.uidPool != nil {
				m.uidPool.Release(id)
			}
			if dirIsNew {
				os.RemoveAll(sessDir)
			}
			return "", fmt.Errorf("creating inference loop for %q: %w", cfg.Name, err)
		}
	}

	ra := &session{
		info: SessionInfo{
			ID:          id,
			Name:        cfg.Name,
			Mode:        mode,
			Description: cfg.Description,
			ParentID:    parentID,
			Status:      SessionStatusRunning,
			Model:       model,
		},
		worker:         handle.Worker,
		handle:         handle,
		loop:           loop,
		effectiveTools: effectiveTools,
	}

	m.mu.Lock()
	m.sessions[id] = ra
	if parentID != "" {
		m.children[parentID] = append(m.children[parentID], id)
	}
	m.mu.Unlock()

	// Start death-watcher goroutine for unexpected process exits.
	go m.watchWorker(id, handle.Done)

	m.logger.Info("session started",
		"id", id,
		"name", cfg.Name,
		"mode", mode,
		"parent", parentID,
	)

	return id, nil
}

// watchWorker monitors a worker's Done channel and handles unexpected exits.
func (m *Manager) watchWorker(agentID string, done <-chan struct{}) {
	<-done

	m.mu.RLock()
	ra, ok := m.sessions[agentID]
	m.mu.RUnlock()
	if !ok || ra.info.Status == SessionStatusStopped {
		return // already removed or intentionally stopped
	}

	m.logger.Warn("session process exited unexpectedly",
		"id", agentID,
		"name", ra.info.Name,
	)

	// Handle the dead agent and its children.
	descendants := m.collectDescendants(agentID)
	for i := len(descendants) - 1; i >= 0; i-- {
		id := descendants[i]
		m.mu.RLock()
		deadRA, exists := m.sessions[id]
		m.mu.RUnlock()
		if !exists || deadRA.info.Status == SessionStatusStopped {
			continue
		}

		if deadRA.info.Mode.IsPersistent() {
			// Persistent sessions become "stopped" — visible but not running.
			// Capture handle under lock and set status atomically to prevent
			// double-cleanup race with softStop.
			m.mu.Lock()
			if deadRA.info.Status == SessionStatusStopped {
				m.mu.Unlock()
				continue // softStop already handled this
			}
			h := deadRA.handle
			deadRA.worker = nil
			deadRA.handle = nil
			deadRA.info.Status = SessionStatusStopped
			m.mu.Unlock()

			m.cleanupWorker(id, h)
			m.setSessionStatus(id, "stopped")
		} else {
			// Ephemeral sessions are fully removed.
			m.mu.Lock()
			h := deadRA.handle
			m.unregisterLocked(id, deadRA)
			m.mu.Unlock()

			m.cleanupWorker(id, h)
			os.RemoveAll(m.sessionDir(id))
		}
	}
}

// removeSession gracefully shuts down and removes an agent from the registry.
// Ephemeral session directories are cleaned up.
func (m *Manager) removeSession(id string) {
	m.mu.Lock()
	ra, ok := m.sessions[id]
	m.unregisterLocked(id, ra)
	m.mu.Unlock()

	if !ok || ra.worker == nil {
		return // not found or already soft-stopped
	}

	m.shutdownWorker(ra)
	m.cleanupWorker(id, ra.handle)

	if !ra.info.Mode.IsPersistent() {
		os.RemoveAll(m.sessionDir(id))
	}
}

// getSession returns the session for the given ID, or nil.
func (m *Manager) getSession(id string) *session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// unregisterLocked removes an agent from the registry and its parent's children
// list. Must be called with m.mu held.
func (m *Manager) unregisterLocked(id string, ra *session) {
	delete(m.sessions, id)
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

	if _, ok := m.sessions[agentID]; !ok {
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

// spawnTool is injected into all agents. Non-coordinators are restricted
// to ephemeral mode at the tool layer.
var spawnTool = "spawn_session"

// coordinatorTools are injected only for coordinator-mode agents. These
// manage persistent agent lifecycles and require hive-coordinators group.
var coordinatorTools = []string{
	"resume_session", "list_sessions", "send_message", "stop_session", "delete_session",
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
		parent, ok := m.sessions[parentID]
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

	// All agents get spawn_session; coordinators can use all modes.
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

// --- Config resolution and push ---

// resolveProvider returns the provider type, API key, and base URL for an agent config.
// Uses the agent's provider override if set, otherwise the default.
func (m *Manager) resolveProvider(cfg config.AgentConfig) (provider, apiKey, baseURL string, err error) {
	if m.cp == nil {
		return "", "", "", nil
	}
	if cfg.Provider != "" {
		apiKey, baseURL, ok := m.cp.ProviderByType(cfg.Provider)
		if !ok {
			return "", "", "", fmt.Errorf("agent %q requests provider %q which is not configured", cfg.Name, cfg.Provider)
		}
		return cfg.Provider, apiKey, baseURL, nil
	}
	provider, apiKey, baseURL, ok := m.cp.ProviderInfo()
	if !ok {
		return "", "", "", fmt.Errorf("no LLM provider configured")
	}
	return provider, apiKey, baseURL, nil
}

// resolveProviderForModel finds which configured provider offers the given model.
// It searches all configured providers and returns the first match.
// Falls back to the default provider if no match is found (allows unknown models).
func (m *Manager) resolveProviderForModel(model string) (provider, apiKey, baseURL string, err error) {
	if m.cp == nil {
		return "", "", "", fmt.Errorf("no control plane configured")
	}
	// Search all configured providers for the model.
	for _, pt := range m.cp.ConfiguredProviderTypes() {
		for _, mi := range models.ModelsForProvider(pt) {
			if mi.ID == model {
				key, bu, ok := m.cp.ProviderByType(pt)
				if ok {
					return pt, key, bu, nil
				}
			}
		}
	}
	return "", "", "", fmt.Errorf("model %q not found in any configured provider", model)
}

// resolveModel returns the resolved model for an agent config.
// Priority: env override → agent config → control plane default.
func (m *Manager) resolveModel(cfg config.AgentConfig) string {
	model := cfg.Model
	if m.cp != nil {
		if dm := m.cp.DefaultModel(); dm != "" && model == "" {
			model = dm
		}
	}
	if m.opts.Model != "" {
		model = m.opts.Model
	}
	return model
}

// WatchAgentDefinitions subscribes to agent.md changes via the filesystem
// watcher and pushes resolved structural config (model, provider, tools,
// description) to affected running agents. Only watches agent.md because
// structural config lives in its YAML frontmatter; text-only files
// (soul.md, tools.md, skills/) are re-read from disk every turn by the
// agent process itself.
func (m *Manager) WatchAgentDefinitions(w *watcher.Watcher) {
	w.Subscribe("agents/*/agent.md", func(events []watcher.Event) {
		seen := make(map[string]bool)
		for _, ev := range events {
			name := extractAgentName(ev.Path)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			m.pushConfigUpdate(name)
		}
	})
}

// pushConfigUpdate reloads an agent definition from disk and pushes the
// resolved config to all running sessions of that agent.
func (m *Manager) pushConfigUpdate(agentName string) {
	// Load config and resolve provider/model outside the lock to avoid
	// disk I/O under the session mutex.
	cfg, err := config.LoadAgentDir(m.agentDefDir(agentName))
	if err != nil {
		m.logger.Warn("failed to load agent definition for config push",
			"agent", agentName, "error", err)
		return
	}

	provider, apiKey, baseURL, err := m.resolveProvider(cfg)
	if err != nil {
		m.logger.Warn("failed to resolve provider for config push",
			"agent", agentName, "error", err)
		return
	}
	model := m.resolveModel(cfg)

	// Check for skills directory (disk I/O, done outside lock).
	hasSkills := len(cfg.Skills) > 0
	if !hasSkills {
		skillsDir := filepath.Join(m.agentDefDir(cfg.Name), "skills")
		if _, err := os.Stat(skillsDir); err == nil {
			hasSkills = true
		}
	}

	// Snapshot running sessions under RLock, then release before pushing.
	type pushTarget struct {
		id           string
		parentID     string
		mode         config.AgentMode
		loop         *inference.Loop
		currentModel string
	}

	m.mu.RLock()
	var targets []pushTarget
	for id, s := range m.sessions {
		if s.info.Name == agentName && s.info.Status == SessionStatusRunning {
			targets = append(targets, pushTarget{id: id, parentID: s.info.ParentID, mode: s.info.Mode, loop: s.loop, currentModel: s.info.Model})
		}
	}
	m.mu.RUnlock()

	for _, t := range targets {
		if t.loop != nil && model != t.currentModel {
			// Apply model changes when the resolved model differs from what's running.
			lm, err := CreateLanguageModel(context.Background(), ProviderType(provider), apiKey, baseURL, model)
			if err != nil {
				m.logger.Warn("failed to create language model for config push",
					"agent", agentName, "model", model, "error", err)
			} else {
				t.loop.UpdateModel(lm, model, provider)
			}
		}
		m.logger.Info("pushed config update to agent",
			"agent", agentName, "session", t.id, "model", model)

		// Update cached info under write lock so API handlers
		// reading SessionInfo don't race.
		m.mu.Lock()
		if s, ok := m.sessions[t.id]; ok {
			s.info.Description = cfg.Description
			s.info.Model = model
		}
		m.mu.Unlock()
	}
}

// PushConfigUpdateAll recomputes and pushes config to all running agents.
// Called when config.yaml changes (provider/model defaults or tool policies may have changed).
func (m *Manager) PushConfigUpdateAll() {
	// Collect unique agent names from running sessions.
	m.mu.RLock()
	names := make(map[string]bool)
	for _, s := range m.sessions {
		if s.info.Status == SessionStatusRunning {
			names[s.info.Name] = true
		}
	}
	m.mu.RUnlock()

	for name := range names {
		m.pushConfigUpdate(name)
	}
}

// extractAgentName extracts the agent name from a watcher path like "agents/foo/agent.md".
func extractAgentName(path string) string {
	// Expected format: "agents/<name>/agent.md" or "agents/<name>/soul.md" etc.
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

func (m *Manager) sessionsDir() string {
	return filepath.Join(m.rootDir, "sessions")
}

func (m *Manager) sessionDir(id string) string {
	return filepath.Join(m.rootDir, "sessions", id)
}

// setSessionStatus updates the session status in the platform database.
// Best-effort: errors are logged but not returned.
func (m *Manager) setSessionStatus(id, status string) {
	if m.pdb == nil {
		return
	}
	if err := m.pdb.UpdateSessionStatus(id, status); err != nil {
		m.logger.Warn("failed to update session status in db", "id", id, "status", status, "error", err)
	}
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
