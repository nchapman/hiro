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

// InstanceStatus represents the lifecycle state of an instance.
type InstanceStatus string

const (
	InstanceStatusRunning InstanceStatus = "running"
	InstanceStatusStopped InstanceStatus = "stopped"
)

// InstanceInfo describes an agent instance for external consumers.
type InstanceInfo struct {
	ID          string
	Name        string
	Mode        config.AgentMode
	Description string
	ParentID    string // empty for top-level instances
	Status      InstanceStatus
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

// instance tracks a live agent instance within the manager.
type instance struct {
	mu             sync.Mutex // serializes calls through the worker
	info           InstanceInfo
	activeSession  string             // current session ID
	worker         ipc.AgentWorker
	handle         *WorkerHandle
	loop           *inference.Loop    // inference loop (runs in control plane)
	effectiveTools map[string]bool    // built-in tools this instance is allowed; nil = unrestricted
	uid            uint32             // isolated UID (0 = no isolation)
	gid            uint32             // isolated GID
	groups         []uint32           // supplementary groups (includes hive-coordinators for coordinators)
}

// Manager supervises agent instance lifecycles on a single node.
type Manager struct {
	mu        sync.RWMutex
	instances map[string]*instance  // instance ID -> running instance
	children  map[string][]string   // parent instance ID -> child instance IDs

	ctx     context.Context // long-lived context for persistent instances
	rootDir string
	opts    Options
	cp      ControlPlane // operator-level tool/secret config
	logger  *slog.Logger

	workerFactory WorkerFactory     // creates agent workers (default = OS processes)
	uidPool       *uidpool.Pool     // per-agent Unix user isolation; nil = disabled
	pdb           *platformdb.DB    // unified platform database
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
// containing agents/, instances/, skills/, and workspace/ subdirectories. The context
// controls the lifetime of persistent instances. cp may be nil if no control
// plane is configured. If wf is nil, the default OS process spawner is used.
func NewManager(ctx context.Context, rootDir string, opts Options, cp ControlPlane, logger *slog.Logger, wf WorkerFactory, pool *uidpool.Pool, pdb *platformdb.DB) *Manager {
	if wf == nil {
		wf = defaultWorkerFactory
	}
	return &Manager{
		instances:     make(map[string]*instance),
		children:      make(map[string][]string),
		ctx:           ctx,
		rootDir:       rootDir,
		opts:          opts,
		cp:            cp,
		logger:        logger,
		workerFactory: wf,
		uidPool:       pool,
		pdb:           pdb,
	}
}

// CreateInstance loads an agent definition by name and starts an instance in the
// given mode. The ctx parameter is used only for config loading and worker
// spawning — persistent/coordinator instances use the manager's lifetime context.
// parentInstanceID tracks lineage; pass "" for top-level instances.
// mode is a string to satisfy the ipc.HostManager interface boundary; it must
// be one of "persistent", "ephemeral", or "coordinator".
func (m *Manager) CreateInstance(ctx context.Context, name, parentInstanceID string, mode string) (string, error) {
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

	instanceID := uuid.Must(uuid.NewV7()).String()
	sessionID := uuid.Must(uuid.NewV7()).String()
	return m.startInstance(ctx, instanceID, sessionID, cfg, parentInstanceID, agentMode)
}

// SpawnEphemeral starts an ephemeral instance that runs the given prompt and returns
// the result. Blocks until the subagent completes. The instance always runs in
// ephemeral mode — the caller controls the lifecycle.
// parentInstanceID identifies the spawning instance (empty for top-level spawns).
// onEvent receives streaming events during execution (may be nil).
func (m *Manager) SpawnEphemeral(ctx context.Context, agentName, prompt, parentInstanceID string, onEvent func(ipc.ChatEvent) error) (string, error) {
	if err := validateAgentName(agentName); err != nil {
		return "", err
	}

	cfg, err := config.LoadAgentDir(m.agentDefDir(agentName))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", agentName, err)
	}

	instanceID := uuid.Must(uuid.NewV7()).String()
	sessionID := uuid.Must(uuid.NewV7()).String()
	instID, err := m.startInstance(ctx, instanceID, sessionID, cfg, parentInstanceID, config.ModeEphemeral)
	if err != nil {
		return "", err
	}

	// Run the prompt and collect the result
	result, err := m.SendMessage(ctx, instID, prompt, onEvent)

	// Clean up the ephemeral instance and its entire subtree
	m.StopInstance(instID)

	if err != nil {
		return "", fmt.Errorf("subagent %q failed: %w", agentName, err)
	}
	return result, nil
}

// UpdateInstanceConfig changes the model and/or reasoning effort for a running instance.
// Changes take effect on the next Chat() call.
func (m *Manager) UpdateInstanceConfig(ctx context.Context, instanceID, model string, reasoningEffort *string) error {
	inst := m.getInstance(instanceID)
	if inst == nil {
		return fmt.Errorf("instance %q not found", instanceID)
	}

	// Serialize with SendMessage to prevent concurrent access to instance state.
	// Status and loop checks must be inside the lock to avoid races with softStop.
	inst.mu.Lock()
	defer inst.mu.Unlock()

	if inst.info.Status == InstanceStatusStopped {
		return fmt.Errorf("instance %q is stopped", instanceID)
	}
	if inst.loop == nil {
		return fmt.Errorf("instance %q has no inference loop", instanceID)
	}

	if model != "" && model != inst.info.Model {
		// Find which configured provider owns this model.
		provider, apiKey, baseURL, err := m.resolveProviderForModel(model)
		if err != nil {
			return err
		}

		lm, err := CreateLanguageModel(ctx, ProviderType(provider), apiKey, baseURL, model)
		if err != nil {
			return fmt.Errorf("creating language model %q: %w", model, err)
		}
		inst.loop.UpdateModel(lm, model, provider)
		inst.info.Model = model
	}

	if reasoningEffort != nil {
		if !validReasoningEffort(*reasoningEffort) {
			return fmt.Errorf("invalid reasoning effort %q", *reasoningEffort)
		}
		inst.loop.SetReasoningEffort(*reasoningEffort)
	}

	// Persist config to DB so it survives restarts.
	if m.pdb != nil {
		cfg := platformdb.InstanceConfig{
			ModelOverride:   inst.info.Model,
			ReasoningEffort: inst.loop.ReasoningEffort(),
		}
		if err := m.pdb.UpdateInstanceConfig(instanceID, cfg); err != nil {
			m.logger.Warn("failed to persist instance config", "instance", instanceID, "error", err)
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
	// send messages in a loop (A → send_message(B) → B sends back to A).
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

// StopInstance stops a running instance and all its descendants.
// Persistent instances are soft-stopped (process killed, kept in registry as "stopped").
// Ephemeral instances are fully removed. Returns the info of the stopped root instance.
func (m *Manager) StopInstance(instanceID string) (ipc.InstanceInfo, error) {
	// Collect the entire subtree in one snapshot, then stop leaf-first
	toStop := m.collectDescendants(instanceID)
	if len(toStop) == 0 {
		return ipc.InstanceInfo{}, fmt.Errorf("instance %q not found", instanceID)
	}

	// Check if already stopped
	rootInfo, _ := m.GetInstance(instanceID)
	if rootInfo.Status == InstanceStatusStopped {
		return m.instanceInfoToIPC(rootInfo), nil
	}

	for i := len(toStop) - 1; i >= 0; i-- {
		id := toStop[i]
		inst := m.getInstance(id)
		if inst == nil || inst.info.Status == InstanceStatusStopped {
			continue
		}
		if inst.info.Mode.IsPersistent() {
			m.softStop(id)
		} else {
			m.removeInstance(id)
		}
		m.logger.Info("instance stopped", "id", id)
	}

	// Re-read info after stop (status may have changed)
	rootInfo, _ = m.GetInstance(instanceID)
	return m.instanceInfoToIPC(rootInfo), nil
}

// DeleteInstance stops and permanently removes an instance and all its descendants.
// Instance directories are always deleted regardless of mode.
func (m *Manager) DeleteInstance(instanceID string) error {
	toStop := m.collectDescendants(instanceID)
	if len(toStop) == 0 {
		return fmt.Errorf("instance %q not found", instanceID)
	}

	for i := len(toStop) - 1; i >= 0; i-- {
		id := toStop[i]
		m.mu.RLock()
		inst, ok := m.instances[id]
		m.mu.RUnlock()
		if !ok {
			continue
		}

		if inst.info.Status == InstanceStatusStopped {
			// Already stopped — just unregister and delete instance dir.
			m.mu.Lock()
			m.unregisterLocked(id, inst)
			m.mu.Unlock()
		} else {
			// Running — do full graceful shutdown + unregister.
			m.removeInstance(id)
		}

		// Always delete instance dir and DB record regardless of mode.
		os.RemoveAll(m.instanceDir(id))
		if m.pdb != nil {
			m.pdb.DeleteInstance(id)
		}
		m.logger.Info("instance deleted", "id", id)
	}
	return nil
}

// StartInstance restarts a stopped persistent instance, creating a new session.
func (m *Manager) StartInstance(ctx context.Context, instanceID string) error {
	m.mu.RLock()
	inst, ok := m.instances[instanceID]
	m.mu.RUnlock()
	if !ok {
		return ErrInstanceNotFound
	}
	if inst.info.Status != InstanceStatusStopped {
		return ErrInstanceNotStopped
	}

	name := inst.info.Name
	parentID := inst.info.ParentID
	mode := inst.info.Mode

	// Remove the stopped entry so startInstance can re-register it.
	m.mu.Lock()
	m.unregisterLocked(instanceID, inst)
	m.mu.Unlock()

	cfg, err := config.LoadAgentDir(m.agentDefDir(name))
	if err != nil {
		// Re-register as stopped so the instance remains visible.
		m.reregisterStopped(instanceID, inst)
		return fmt.Errorf("loading agent %q: %w", name, err)
	}

	// Resume the latest session if one exists, otherwise create a new one.
	var sessionID string
	if m.pdb != nil {
		if sess, ok := m.pdb.LatestSessionByInstance(instanceID); ok {
			sessionID = sess.ID
		}
	}
	if sessionID == "" {
		sessionID = uuid.Must(uuid.NewV7()).String()
	}
	if _, err = m.startInstance(ctx, instanceID, sessionID, cfg, parentID, mode); err != nil {
		// Re-register as stopped so the instance remains visible.
		m.reregisterStopped(instanceID, inst)
		return err
	}

	// Clear the stopped flag so the instance starts on next server restart.
	m.setInstanceStatus(instanceID, "running")
	return nil
}

// NewSession ends the current session and starts a new one within the same instance.
// This is the /clear handler — memory and identity persist, messages and todos reset.
func (m *Manager) NewSession(instanceID string) (string, error) {
	inst := m.getInstance(instanceID)
	if inst == nil {
		return "", ErrInstanceNotFound
	}

	inst.mu.Lock()
	defer inst.mu.Unlock()

	// Status check inside lock to avoid race with softStop/watchWorker.
	if inst.info.Status == InstanceStatusStopped {
		return "", fmt.Errorf("instance %q is stopped", instanceID)
	}

	// Capture and nil out handle before shutdown so the old watchWorker
	// goroutine sees nil and bails out instead of tearing down the instance.
	oldHandle := inst.handle
	inst.worker = nil
	inst.handle = nil
	inst.loop = nil

	// Shut down the old worker (blocks until exit).
	m.shutdownHandle(oldHandle)
	if oldHandle != nil {
		oldHandle.Close()
	}

	// Mark the old session as stopped in DB.
	if m.pdb != nil && inst.activeSession != "" {
		m.pdb.UpdateSessionStatus(inst.activeSession, "stopped")
	}

	// Create new session directory.
	newSessionID := uuid.Must(uuid.NewV7()).String()
	sessDir := m.instanceSessionDir(instanceID, newSessionID)
	if err := os.MkdirAll(sessDir, 0700); err != nil {
		return "", fmt.Errorf("creating session dir: %w", err)
	}
	for _, sub := range []string{"scratch", "tmp"} {
		if err := os.MkdirAll(filepath.Join(sessDir, sub), 0700); err != nil {
			return "", fmt.Errorf("creating session %s dir: %w", sub, err)
		}
	}

	// Register new session in DB.
	if m.pdb != nil {
		if err := m.pdb.CreateSession(platformdb.Session{
			ID:         newSessionID,
			InstanceID: instanceID,
			AgentName:  inst.info.Name,
			Mode:       string(inst.info.Mode),
		}); err != nil {
			return "", fmt.Errorf("creating session in db: %w", err)
		}
	}

	// Chown session dir using the UID already held for this instance.
	if inst.uid != 0 {
		filepath.WalkDir(sessDir, func(path string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			return os.Chown(path, int(inst.uid), int(inst.gid))
		})
	}

	// Reload agent config and resolve provider.
	cfg, err := config.LoadAgentDir(m.agentDefDir(inst.info.Name))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", inst.info.Name, err)
	}

	provider, apiKey, baseURL, err := m.resolveProvider(cfg)
	if err != nil {
		return "", err
	}
	model := m.resolveModel(cfg)
	cfg.Model = model

	hasSkills := len(cfg.Skills) > 0
	if !hasSkills {
		skillsDir := filepath.Join(m.agentDefDir(cfg.Name), "skills")
		if _, err := os.Stat(skillsDir); err == nil {
			hasSkills = true
		}
	}
	allowedTools := buildAllowedToolsMap(inst.effectiveTools, inst.info.Mode, hasSkills)

	spawnCtx := m.ctx // persistent instances always use manager context

	spawnCfg := ipc.SpawnConfig{
		InstanceID:     instanceID,
		SessionID:      newSessionID,
		AgentName:      cfg.Name,
		EffectiveTools: allowedTools,
		WorkingDir:     m.opts.WorkingDir,
		SessionDir:     sessDir,
		AgentSocket:    filepath.Join(os.TempDir(), fmt.Sprintf("hive-agent-%s.sock", newSessionID)),
		UID:            inst.uid,
		GID:            inst.gid,
		Groups:         inst.groups,
	}

	// failStopped marks the instance as stopped on spawn/loop failure.
	// The old worker is already dead, so there's nothing to recover to.
	failStopped := func(err error) (string, error) {
		inst.info.Status = InstanceStatusStopped
		m.setInstanceStatus(instanceID, "stopped")
		os.RemoveAll(sessDir)
		if m.pdb != nil {
			m.pdb.DeleteSession(newSessionID)
		}
		return "", err
	}

	handle, err := m.workerFactory(spawnCtx, spawnCfg)
	if err != nil {
		return failStopped(fmt.Errorf("spawning agent %q: %w", cfg.Name, err))
	}
	if wc, ok := handle.Worker.(*grpcipc.WorkerClient); ok {
		wc.SetSecretEnvFn(m.SecretEnv)
	}

	// Create new inference loop.
	var loop *inference.Loop
	if provider != "" {
		lm, err := CreateLanguageModel(spawnCtx, ProviderType(provider), apiKey, baseURL, model)
		if err != nil {
			handle.Kill()
			return failStopped(fmt.Errorf("creating language model for %q: %w", cfg.Name, err))
		}

		loop, err = inference.NewLoop(inference.LoopConfig{
			InstanceID:     instanceID,
			SessionID:      newSessionID,
			AgentConfig:    cfg,
			Mode:           inst.info.Mode,
			WorkingDir:     m.opts.WorkingDir,
			InstanceDir:    m.instanceDir(instanceID),
			SessionDir:     sessDir,
			AgentDefDir:    m.agentDefDir(cfg.Name),
			SharedSkillDir: m.sharedSkillsDir(),
			LM:             lm,
			Provider:       provider,
			Executor:       handle.Worker,
			PDB:            m.pdb,
			AllowedTools:   allowedTools,
			HasSkills:      hasSkills,
			SecretNamesFn:  m.SecretNames,
			SecretEnvFn:    m.SecretEnv,
			Logger:         m.logger.With("instance", instanceID, "session", newSessionID, "agent", cfg.Name),
			HostManager:    m,
			CallerMode:     inst.info.Mode,
		})
		if err != nil {
			handle.Kill()
			return failStopped(fmt.Errorf("creating inference loop for %q: %w", cfg.Name, err))
		}
	}

	inst.activeSession = newSessionID
	inst.worker = handle.Worker
	inst.handle = handle
	inst.loop = loop

	go m.watchWorker(instanceID, handle.Done)

	m.logger.Info("new session created",
		"instance", instanceID,
		"session", newSessionID,
		"agent", inst.info.Name,
	)

	return newSessionID, nil
}

// shutdownGrace is the deadline for a graceful worker shutdown before force-killing.
const shutdownGrace = 5 * time.Second

// shutdownHandle sends a graceful shutdown to a worker handle, waits for exit
// under a deadline, and force-kills if necessary.
func (m *Manager) shutdownHandle(h *WorkerHandle) {
	if h == nil || h.Worker == nil {
		return
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	h.Worker.Shutdown(shutCtx)

	select {
	case <-h.Done:
		// Process exited cleanly.
	case <-shutCtx.Done():
		// Deadline expired — force-kill and wait.
		if h.Kill != nil {
			h.Kill()
		}
		<-h.Done
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

// softStop gracefully shuts down a persistent instance's worker process
// but keeps it in the registry with status "stopped".
func (m *Manager) softStop(id string) {
	// Capture the handle under both locks and mark stopped atomically.
	// Lock order: m.mu → inst.mu (no reverse path exists in the codebase).
	// Both locks are needed: m.mu for watchWorker, inst.mu for SendMessage/UpdateConfig.
	m.mu.Lock()
	inst, ok := m.instances[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	inst.mu.Lock()
	h := inst.handle
	inst.worker = nil
	inst.handle = nil
	inst.loop = nil
	inst.info.Status = InstanceStatusStopped
	inst.mu.Unlock()
	m.mu.Unlock()

	// Shutdown the captured handle outside the lock (blocks on I/O).
	m.shutdownHandle(h)
	m.cleanupWorker(id, h)

	// Persist stopped state so it survives server restarts.
	m.setInstanceStatus(id, "stopped")
}

// reregisterStopped puts an instance back into the registry as stopped.
// Used when StartInstance fails after unregistering.
func (m *Manager) reregisterStopped(id string, inst *instance) {
	m.mu.Lock()
	inst.info.Status = InstanceStatusStopped
	m.instances[id] = inst
	if inst.info.ParentID != "" {
		m.children[inst.info.ParentID] = append(m.children[inst.info.ParentID], id)
	}
	m.mu.Unlock()
}

// instanceInfoToIPC converts an InstanceInfo to ipc.InstanceInfo.
func (m *Manager) instanceInfoToIPC(info InstanceInfo) ipc.InstanceInfo {
	return ipc.InstanceInfo{
		ID:          info.ID,
		Name:        info.Name,
		Mode:        string(info.Mode),
		Description: info.Description,
		ParentID:    info.ParentID,
		Status:      string(info.Status),
		Model:       info.Model,
	}
}

// GetInstance returns info about an instance (running or stopped).
func (m *Manager) GetInstance(instanceID string) (InstanceInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[instanceID]
	if !ok {
		return InstanceInfo{}, false
	}
	return inst.info, true
}

// ListInstances returns a snapshot of all instances (running and stopped) sorted by creation order.
// Instance IDs are UUIDv7 (time-ordered), so lexicographic sort gives creation order.
func (m *Manager) ListInstances() []InstanceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]InstanceInfo, 0, len(m.instances))
	for _, inst := range m.instances {
		result = append(result, inst.info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// ListChildInstances returns a snapshot of instances that are direct children of callerInstanceID.
func (m *Manager) ListChildInstances(callerInstanceID string) []ipc.InstanceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	childIDs := m.children[callerInstanceID]
	result := make([]ipc.InstanceInfo, 0, len(childIDs))
	for _, cid := range childIDs {
		if inst, ok := m.instances[cid]; ok {
			result = append(result, ipc.InstanceInfo{
				ID:          inst.info.ID,
				Name:        inst.info.Name,
				Mode:        string(inst.info.Mode),
				Description: inst.info.Description,
				ParentID:    inst.info.ParentID,
				Status:      string(inst.info.Status),
				Model:       inst.info.Model,
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

// ErrInstanceNotFound is returned when an instance ID does not match a known instance.
var ErrInstanceNotFound = errors.New("instance not found")

// ErrInstanceNotStopped is returned when an operation requires a stopped instance.
var ErrInstanceNotStopped = errors.New("instance is not stopped")

// GetHistory returns recent messages from the active session of a persistent instance.
func (m *Manager) GetHistory(instanceID string, limit int) ([]HistoryMessage, error) {
	m.mu.RLock()
	inst, ok := m.instances[instanceID]
	var sessionID string
	var persistent bool
	if ok {
		sessionID = inst.activeSession
		persistent = inst.info.Mode.IsPersistent()
	}
	m.mu.RUnlock()
	if !ok {
		return nil, ErrInstanceNotFound
	}

	if !persistent {
		return nil, nil
	}

	if m.pdb == nil {
		return nil, nil
	}
	if sessionID == "" {
		return nil, nil
	}

	msgs, err := m.pdb.RecentMessages(sessionID, limit)
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

// GetSessionHistory returns recent messages from a specific session (for history browsing).
func (m *Manager) GetSessionHistory(sessionID string, limit int) ([]HistoryMessage, error) {
	if m.pdb == nil {
		return nil, nil
	}

	msgs, err := m.pdb.RecentMessages(sessionID, limit)
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

// InstanceByAgentName returns the ID and status of an instance by agent name.
// Prefers running instances; falls back to stopped ones. If multiple
// instances share a name, running ones take priority.
func (m *Manager) InstanceByAgentName(name string) (id string, running bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for iid, inst := range m.instances {
		if inst.info.Name == name {
			if inst.info.Status == InstanceStatusRunning {
				return iid, true
			}
			// Remember the stopped one, but keep looking for a running one.
			id = iid
		}
	}
	return id, false
}

// ActiveSessionID returns the active session ID for an instance.
func (m *Manager) ActiveSessionID(instanceID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.instances[instanceID]; ok {
		return inst.activeSession
	}
	return ""
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

// RestoreInstances reads persistent/coordinator instances from the platform
// database and restarts them. Call once after NewManager.
func (m *Manager) RestoreInstances(ctx context.Context) error {
	if m.pdb == nil {
		return nil
	}

	instances, err := m.pdb.ListInstances("", "")
	if err != nil {
		return fmt.Errorf("listing instances from db: %w", err)
	}

	for _, dbInst := range instances {
		mode := config.AgentMode(dbInst.Mode)
		if !mode.IsPersistent() {
			// Clean up stale ephemeral instances from db and disk.
			m.pdb.DeleteInstance(dbInst.ID)
			os.RemoveAll(m.instanceDir(dbInst.ID))
			continue
		}

		if err := validateAgentName(dbInst.AgentName); err != nil {
			m.logger.Warn("skipping instance with invalid agent name",
				"id", dbInst.ID, "agent", dbInst.AgentName, "error", err)
			continue
		}

		cfg, err := config.LoadAgentDir(m.agentDefDir(dbInst.AgentName))
		if err != nil {
			m.logger.Warn("skipping instance with missing agent definition",
				"agent", dbInst.AgentName, "error", err)
			continue
		}

		if dbInst.Status == "stopped" {
			inst := &instance{
				info: InstanceInfo{
					ID:          dbInst.ID,
					Name:        cfg.Name,
					Mode:        mode,
					Description: cfg.Description,
					ParentID:    dbInst.ParentID,
					Status:      InstanceStatusStopped,
					Model:       m.resolveModel(cfg),
				},
			}
			m.mu.Lock()
			m.instances[dbInst.ID] = inst
			if dbInst.ParentID != "" {
				m.children[dbInst.ParentID] = append(m.children[dbInst.ParentID], dbInst.ID)
			}
			m.mu.Unlock()
			m.logger.Info("restored stopped instance",
				"id", dbInst.ID, "name", cfg.Name)
			continue
		}

		// Verify instance dir exists.
		if _, err := os.Stat(m.instanceDir(dbInst.ID)); os.IsNotExist(err) {
			m.logger.Warn("instance dir missing, removing orphaned DB record",
				"id", dbInst.ID, "agent", dbInst.AgentName)
			m.pdb.DeleteInstance(dbInst.ID)
			continue
		}

		// Resume the latest session if one exists, otherwise create a new one.
		var sessionID string
		if sess, ok := m.pdb.LatestSessionByInstance(dbInst.ID); ok {
			sessionID = sess.ID
			m.logger.Info("resuming existing session",
				"instance", dbInst.ID, "session", sessionID, "agent", dbInst.AgentName)
		} else {
			sessionID = uuid.Must(uuid.NewV7()).String()
			m.logger.Info("creating new session (no previous session found)",
				"instance", dbInst.ID, "session", sessionID, "agent", dbInst.AgentName)
		}
		_, err = m.startInstance(ctx, dbInst.ID, sessionID, cfg, dbInst.ParentID, mode)
		if err != nil {
			m.logger.Warn("failed to restore instance",
				"id", dbInst.ID, "agent", dbInst.AgentName, "error", err)
			continue
		}

		// Restore per-instance config (model override, reasoning effort).
		if instCfg, cfgErr := m.pdb.GetInstanceConfig(dbInst.ID); cfgErr == nil {
			if instCfg.ModelOverride != "" || instCfg.ReasoningEffort != "" {
				effort := instCfg.ReasoningEffort
				_ = m.UpdateInstanceConfig(ctx, dbInst.ID, instCfg.ModelOverride, &effort)
			}
		}
	}
	return nil
}

// Shutdown stops all running instances. Ephemeral instance directories are cleaned up.
// Stopped instances are unregistered without attempting worker shutdown.
func (m *Manager) Shutdown() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.instances))
	for id := range m.instances {
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
		inst := m.getInstance(id)
		if inst != nil && inst.info.Status == InstanceStatusStopped {
			// Already stopped — just unregister.
			m.mu.Lock()
			m.unregisterLocked(id, inst)
			m.mu.Unlock()
			continue
		}
		m.removeInstance(id)
	}

	m.logger.Info("instance manager shut down")
}

// startInstance creates instance and session directories, spawns a worker process,
// and registers the instance in the manager.
func (m *Manager) startInstance(ctx context.Context, instanceID, sessionID string, cfg config.AgentConfig, parentID string, mode config.AgentMode) (string, error) {
	// Create instance directory with instance-level state.
	instDir := m.instanceDir(instanceID)
	_, statErr := os.Stat(instDir)
	dirIsNew := os.IsNotExist(statErr)
	if err := os.MkdirAll(instDir, 0700); err != nil {
		return "", fmt.Errorf("creating instance dir: %w", err)
	}

	// Create session directory with session-level state.
	sessDir := m.instanceSessionDir(instanceID, sessionID)
	if err := os.MkdirAll(sessDir, 0700); err != nil {
		return "", fmt.Errorf("creating session dir: %w", err)
	}
	for _, sub := range []string{"scratch", "tmp"} {
		if err := os.MkdirAll(filepath.Join(sessDir, sub), 0700); err != nil {
			return "", fmt.Errorf("creating session %s dir: %w", sub, err)
		}
	}

	// Register instance in the platform database.
	if m.pdb != nil {
		if err := m.pdb.CreateInstance(platformdb.Instance{
			ID:        instanceID,
			AgentName: cfg.Name,
			Mode:      string(mode),
			ParentID:  parentID,
		}); err != nil && !errors.Is(err, platformdb.ErrDuplicate) {
			return "", fmt.Errorf("creating instance in db: %w", err)
		}

		// Register session in the platform database.
		// Note: session parent_id is left empty — lineage is tracked via instances.parent_id.
		if err := m.pdb.CreateSession(platformdb.Session{
			ID:         sessionID,
			InstanceID: instanceID,
			AgentName:  cfg.Name,
			Mode:       string(mode),
		}); err != nil && !errors.Is(err, platformdb.ErrDuplicate) {
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

	// Persistent instances use the manager's long-lived context so they
	// survive beyond the tool call that started them. Ephemeral instances
	// use the caller's context (typically the parent's tool call).
	spawnCtx := ctx
	if mode.IsPersistent() {
		spawnCtx = m.ctx
	}

	// Acquire a dedicated Unix UID for this instance (if isolation is enabled).
	var uid, gid uint32
	var groups []uint32
	if m.uidPool != nil {
		var err error
		uid, gid, err = m.uidPool.Acquire(instanceID)
		if err != nil {
			return "", fmt.Errorf("acquiring UID: %w", err)
		}
		groups = []uint32{gid}
		// Coordinator instances get write access to agents/ and skills/.
		if mode == config.ModeCoordinator {
			if coordGID := m.uidPool.CoordinatorGID(); coordGID != 0 {
				groups = append(groups, coordGID)
			}
		}
		// Transfer ownership of the instance dir (and all contents) to the agent user.
		if err := filepath.WalkDir(instDir, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Chown(path, int(uid), int(gid))
		}); err != nil {
			if os.IsPermission(err) {
				m.logger.Warn("cannot chown instance dir (not root); file isolation degraded",
					"instance", instanceID, "uid", uid)
			} else {
				m.uidPool.Release(instanceID)
				return "", fmt.Errorf("chowning instance dir: %w", err)
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
		InstanceID:     instanceID,
		SessionID:      sessionID,
		AgentName:      cfg.Name,
		EffectiveTools: allowedTools,
		WorkingDir:     m.opts.WorkingDir,
		SessionDir:     sessDir,
		AgentSocket:    filepath.Join(os.TempDir(), fmt.Sprintf("hive-agent-%s.sock", sessionID)),
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
	// cleanup removes directories and releases UID on failure.
	cleanup := func() {
		os.RemoveAll(sessDir) // always clean the session dir
		if dirIsNew {
			os.RemoveAll(instDir)
		}
		if m.uidPool != nil {
			m.uidPool.Release(instanceID)
		}
	}

	if err != nil {
		cleanup()
		return "", fmt.Errorf("spawning agent %q: %w", cfg.Name, err)
	}

	// Create the inference loop (skipped if no provider — test mode).
	var loop *inference.Loop
	if provider != "" {
		lm, err := CreateLanguageModel(spawnCtx, ProviderType(provider), apiKey, baseURL, model)
		if err != nil {
			handle.Kill()
			cleanup()
			return "", fmt.Errorf("creating language model for %q: %w", cfg.Name, err)
		}

		loop, err = inference.NewLoop(inference.LoopConfig{
			InstanceID:     instanceID,
			SessionID:      sessionID,
			AgentConfig:    cfg,
			Mode:           mode,
			WorkingDir:     m.opts.WorkingDir,
			InstanceDir:    instDir,
			SessionDir:     sessDir,
			AgentDefDir:    m.agentDefDir(cfg.Name),
			SharedSkillDir: m.sharedSkillsDir(),
			LM:             lm,
			Provider:       provider,
			Executor:       handle.Worker,
			PDB:            m.pdb,
			AllowedTools:   allowedTools,
			HasSkills:      hasSkills,
			SecretNamesFn:  m.SecretNames,
			SecretEnvFn:    m.SecretEnv,
			Logger:         m.logger.With("instance", instanceID, "session", sessionID, "agent", cfg.Name),
			HostManager:    m,
			CallerMode:     mode,
		})
		if err != nil {
			handle.Kill()
			cleanup()
			return "", fmt.Errorf("creating inference loop for %q: %w", cfg.Name, err)
		}
	}

	inst := &instance{
		info: InstanceInfo{
			ID:          instanceID,
			Name:        cfg.Name,
			Mode:        mode,
			Description: cfg.Description,
			ParentID:    parentID,
			Status:      InstanceStatusRunning,
			Model:       model,
		},
		activeSession:  sessionID,
		worker:         handle.Worker,
		handle:         handle,
		loop:           loop,
		effectiveTools: effectiveTools,
		uid:            uid,
		gid:            gid,
		groups:         groups,
	}

	m.mu.Lock()
	m.instances[instanceID] = inst
	if parentID != "" {
		m.children[parentID] = append(m.children[parentID], instanceID)
	}
	m.mu.Unlock()

	// Start death-watcher goroutine for unexpected process exits.
	go m.watchWorker(instanceID, handle.Done)

	m.logger.Info("instance started",
		"id", instanceID,
		"session", sessionID,
		"name", cfg.Name,
		"mode", mode,
		"parent", parentID,
	)

	return instanceID, nil
}

// watchWorker monitors a worker's Done channel and handles unexpected exits.
func (m *Manager) watchWorker(instanceID string, done <-chan struct{}) {
	<-done

	m.mu.RLock()
	inst, ok := m.instances[instanceID]
	// Bail out if the instance was removed, stopped, or if the handle was
	// cleared by NewSession (which nils handle before shutting down the old
	// worker to prevent this goroutine from interfering with the new session).
	stale := !ok || inst.info.Status == InstanceStatusStopped || inst.handle == nil
	var name string
	if ok {
		name = inst.info.Name
	}
	m.mu.RUnlock()
	if stale {
		return
	}

	m.logger.Warn("instance process exited unexpectedly",
		"id", instanceID,
		"name", name,
	)

	// Handle the dead instance and its children.
	descendants := m.collectDescendants(instanceID)
	for i := len(descendants) - 1; i >= 0; i-- {
		id := descendants[i]
		m.mu.RLock()
		deadInst, exists := m.instances[id]
		m.mu.RUnlock()
		if !exists || deadInst.info.Status == InstanceStatusStopped {
			continue
		}

		if deadInst.info.Mode.IsPersistent() {
			m.mu.Lock()
			deadInst.mu.Lock()
			if deadInst.info.Status == InstanceStatusStopped {
				deadInst.mu.Unlock()
				m.mu.Unlock()
				continue
			}
			h := deadInst.handle
			deadInst.worker = nil
			deadInst.handle = nil
			deadInst.loop = nil
			deadInst.info.Status = InstanceStatusStopped
			deadInst.mu.Unlock()
			m.mu.Unlock()

			m.cleanupWorker(id, h)
			m.setInstanceStatus(id, "stopped")
		} else {
			m.mu.Lock()
			h := deadInst.handle
			deadInst.worker = nil
			deadInst.handle = nil
			deadInst.loop = nil
			m.unregisterLocked(id, deadInst)
			m.mu.Unlock()

			m.cleanupWorker(id, h)
			os.RemoveAll(m.instanceDir(id))
		}
	}
}

// removeInstance gracefully shuts down and removes an instance from the registry.
// Ephemeral instance directories are cleaned up.
func (m *Manager) removeInstance(id string) {
	m.mu.Lock()
	inst, ok := m.instances[id]
	var h *WorkerHandle
	if ok {
		inst.mu.Lock()
		h = inst.handle
		inst.worker = nil
		inst.handle = nil
		inst.loop = nil
		inst.mu.Unlock()
	}
	m.unregisterLocked(id, inst)
	m.mu.Unlock()

	if !ok {
		return
	}

	m.shutdownHandle(h)
	m.cleanupWorker(id, h)

	if !inst.info.Mode.IsPersistent() {
		os.RemoveAll(m.instanceDir(id))
	}
}

// getInstance returns the instance for the given ID, or nil.
func (m *Manager) getInstance(id string) *instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instances[id]
}

// unregisterLocked removes an instance from the registry and its parent's children
// list. Must be called with m.mu held.
func (m *Manager) unregisterLocked(id string, inst *instance) {
	delete(m.instances, id)
	delete(m.children, id)
	if inst != nil && inst.info.ParentID != "" {
		siblings := m.children[inst.info.ParentID]
		updated := make([]string, 0, len(siblings))
		for _, cid := range siblings {
			if cid != id {
				updated = append(updated, cid)
			}
		}
		m.children[inst.info.ParentID] = updated
	}
}

// collectDescendants returns instanceID plus all its descendants via BFS,
// in parent-first order (reverse for leaf-first shutdown).
func (m *Manager) collectDescendants(instanceID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.instances[instanceID]; !ok {
		return nil
	}

	var result []string
	queue := []string{instanceID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		result = append(result, current)
		queue = append(queue, m.children[current]...)
	}
	return result
}

// spawnTool is injected into all agents.
var spawnTool = "spawn_instance"

// coordinatorTools are injected only for coordinator-mode instances.
var coordinatorTools = []string{
	"resume_instance", "list_instances", "send_message", "stop_instance", "delete_instance",
}

// persistentTools are injected for persistent and coordinator instances.
var persistentTools = []string{
	"memory_read", "memory_write", "todos", "history_search", "history_recall",
}

// computeEffectiveTools returns the set of built-in tools this instance is
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
		parent, ok := m.instances[parentID]
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

	// All instances get spawn_instance; coordinators can use all modes.
	allowed[spawnTool] = true

	// Coordinator instances get full instance management tools.
	if mode == config.ModeCoordinator {
		for _, t := range coordinatorTools {
			allowed[t] = true
		}
	}

	// Persistent and coordinator instances get memory/todos/history tools.
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
func (m *Manager) resolveProviderForModel(model string) (provider, apiKey, baseURL string, err error) {
	if m.cp == nil {
		return "", "", "", fmt.Errorf("no control plane configured")
	}
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
// watcher and pushes resolved structural config to affected running instances.
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
// resolved config to all running instances of that agent.
func (m *Manager) pushConfigUpdate(agentName string) {
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

	hasSkills := len(cfg.Skills) > 0
	if !hasSkills {
		skillsDir := filepath.Join(m.agentDefDir(cfg.Name), "skills")
		if _, err := os.Stat(skillsDir); err == nil {
			hasSkills = true
		}
	}

	type pushTarget struct {
		id           string
		parentID     string
		mode         config.AgentMode
		loop         *inference.Loop
		currentModel string
	}

	m.mu.RLock()
	var targets []pushTarget
	for id, inst := range m.instances {
		if inst.info.Name == agentName && inst.info.Status == InstanceStatusRunning {
			targets = append(targets, pushTarget{id: id, parentID: inst.info.ParentID, mode: inst.info.Mode, loop: inst.loop, currentModel: inst.info.Model})
		}
	}
	m.mu.RUnlock()

	for _, t := range targets {
		inst := m.getInstance(t.id)
		if inst == nil {
			continue // removed between snapshot and push
		}

		inst.mu.Lock()
		if inst.info.Status == InstanceStatusStopped {
			inst.mu.Unlock()
			continue
		}
		// Model switch requires a live loop; description is always updated.
		if inst.loop != nil && model != inst.info.Model {
			lm, err := CreateLanguageModel(context.Background(), ProviderType(provider), apiKey, baseURL, model)
			if err != nil {
				m.logger.Warn("failed to create language model for config push",
					"agent", agentName, "model", model, "error", err)
			} else {
				inst.loop.UpdateModel(lm, model, provider)
				inst.info.Model = model
			}
		}
		inst.info.Description = cfg.Description
		inst.mu.Unlock()

		m.logger.Info("pushed config update to instance",
			"agent", agentName, "instance", t.id, "model", model)
	}
}

// PushConfigUpdateAll recomputes and pushes config to all running instances.
func (m *Manager) PushConfigUpdateAll() {
	m.mu.RLock()
	names := make(map[string]bool)
	for _, inst := range m.instances {
		if inst.info.Status == InstanceStatusRunning {
			names[inst.info.Name] = true
		}
	}
	m.mu.RUnlock()

	for name := range names {
		m.pushConfigUpdate(name)
	}
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
