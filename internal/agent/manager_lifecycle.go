package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nchapman/hiro/internal/platform/fsperm"

	"github.com/google/uuid"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/models"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/provider"
	"github.com/nchapman/hiro/internal/toolrules"
)

// CreateInstance loads an agent definition by name and starts an instance in the
// given mode. The ctx parameter is used only for config loading and worker
// spawning — persistent/operator instances use the manager's lifetime context.
// parentInstanceID tracks lineage; pass "" for top-level instances.
// mode is a string to satisfy the ipc.HostManager interface boundary; it must
// be one of "persistent" or "ephemeral".
// displayName and displayDesc override the agent definition name/description
// in persona.md frontmatter (pass "" to use defaults).
func (m *Manager) CreateInstance(ctx context.Context, name, parentInstanceID, mode string, nodeID ipc.NodeID, displayName, displayDesc, personaBody string) (string, error) {
	if err := validateAgentName(name); err != nil {
		return "", err
	}

	agentMode := config.AgentMode(mode)
	switch agentMode {
	case config.ModePersistent, config.ModeEphemeral:
		// valid
	default:
		return "", fmt.Errorf("invalid mode %q: must be persistent or ephemeral", mode)
	}

	cfg, err := config.LoadAgentDir(m.agentDefDir(name))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", name, err)
	}

	// Determine initial channel key: ephemeral instances get an agent channel
	// keyed by the parent; persistent instances default to channelWeb (the operator
	// bootstrap case). Channels will create their own sessions via EnsureSession.
	channelKey := channelWeb
	if parentInstanceID != "" {
		channelKey = "agent:" + parentInstanceID
	}

	instanceID := uuid.Must(uuid.NewV7()).String()
	sessionID := uuid.Must(uuid.NewV7()).String()
	m.logger.Info("creating instance", "instance_id", instanceID, "agent", name, "mode", mode)
	id, err2 := m.startInstance(ctx, instanceID, sessionID, channelKey, cfg, parentInstanceID, agentMode, nodeID, displayName, displayDesc, personaBody)
	if err2 != nil {
		m.logger.Error("instance creation failed", "instance_id", instanceID, "agent", name, "error", err2)
	}
	return id, err2
}

// SpawnEphemeral starts an ephemeral instance that runs the given prompt and returns
// the result. Blocks until the subagent completes. The instance always runs in
// ephemeral mode — the caller controls the lifecycle.
// parentInstanceID identifies the spawning instance (empty for top-level spawns).
// onEvent receives streaming events during execution (may be nil).
func (m *Manager) SpawnEphemeral(ctx context.Context, agentName, prompt, parentInstanceID string, nodeID ipc.NodeID, onEvent func(ipc.ChatEvent) error) (string, error) {
	if err := validateAgentName(agentName); err != nil {
		return "", err
	}

	cfg, err := config.LoadAgentDir(m.agentDefDir(agentName))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", agentName, err)
	}

	channelKey := channelWeb
	if parentInstanceID != "" {
		channelKey = "agent:" + parentInstanceID
	}
	instanceID := uuid.Must(uuid.NewV7()).String()
	sessionID := uuid.Must(uuid.NewV7()).String()
	m.logger.Info("spawning ephemeral", "instance_id", instanceID, "agent", agentName)
	instID, err := m.startInstance(ctx, instanceID, sessionID, channelKey, cfg, parentInstanceID, config.ModeEphemeral, nodeID, "", "", "")
	if err != nil {
		return "", err
	}

	// Run the prompt and collect the result
	result, err := m.SendMessage(ctx, instID, prompt, onEvent)

	// Clean up the ephemeral instance and its entire subtree
	_, _ = m.StopInstance(instID)

	if err != nil {
		return "", fmt.Errorf("subagent %q failed: %w", agentName, err)
	}
	return result, nil
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
		m.logger.Info("instance stopped", "instance_id", id)
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

		// Remove any scheduler entries for this instance.
		if m.scheduler != nil {
			m.scheduler.PauseInstance(context.Background(), id)
		}

		// Always delete instance dir, config file, and DB record regardless of mode.
		os.RemoveAll(m.instanceDir(id))
		os.Remove(m.instanceConfigPath(id))
		if m.pdb != nil {
			if err := m.pdb.DeleteInstance(context.Background(), id); err != nil {
				m.logger.Warn("failed to delete instance from DB", "id", id, "error", err)
			}
		}
		m.logger.Info("instance deleted", "id", id)
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

// seedInstanceFiles creates persona.md and memory.md in a new instance directory,
// and writes the instance config to configPath (outside the instance dir).
// The config is seeded with the agent's tool declarations so the instance owns its own tool config.
func seedInstanceFiles(instDir, configPath string, mode config.AgentMode, displayName, displayDesc, personaBody string, allowedTools, disallowedTools []string) error {
	if mode.IsPersistent() && (displayName != "" || displayDesc != "" || personaBody != "") {
		if err := config.WritePersonaFile(instDir, displayName, displayDesc, personaBody); err != nil {
			return fmt.Errorf("creating persona.md: %w", err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(instDir, "persona.md"), nil, fsperm.FilePrivate); err != nil {
			return fmt.Errorf("creating persona.md: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(instDir, "memory.md"), nil, fsperm.FilePrivate); err != nil {
		return fmt.Errorf("creating memory.md: %w", err)
	}
	if err := config.SaveInstanceConfig(configPath, config.InstanceConfig{
		AllowedTools:    allowedTools,
		DisallowedTools: disallowedTools,
	}); err != nil {
		return fmt.Errorf("creating instance config: %w", err)
	}
	return nil
}

// createSessionDirs creates the session directory and its scratch/tmp subdirectories.
func createSessionDirs(sessDir string) error {
	if err := os.MkdirAll(sessDir, fsperm.DirPrivate); err != nil {
		return fmt.Errorf("creating session dir: %w", err)
	}
	for _, sub := range []string{"scratch", "tmp"} {
		if err := os.MkdirAll(filepath.Join(sessDir, sub), fsperm.DirPrivate); err != nil {
			return fmt.Errorf("creating session %s dir: %w", sub, err)
		}
	}
	return nil
}

// registerInstanceInDB creates instance and session records in the platform database.
func (m *Manager) registerInstanceInDB(ctx context.Context, instanceID, sessionID, channelType, channelID string, cfg config.AgentConfig, mode config.AgentMode, parentID string, nodeID ipc.NodeID) error {
	if m.pdb == nil {
		return nil
	}
	if err := m.pdb.CreateInstance(ctx, platformdb.Instance{
		ID:        instanceID,
		AgentName: cfg.Name,
		Mode:      string(mode),
		ParentID:  parentID,
		NodeID:    nodeID,
	}); err != nil && !errors.Is(err, platformdb.ErrDuplicate) {
		return fmt.Errorf("creating instance in db: %w", err)
	}
	// Note: session parent_id is left empty — lineage is tracked via instances.parent_id.
	if err := m.pdb.CreateSession(ctx, platformdb.Session{
		ID:          sessionID,
		InstanceID:  instanceID,
		AgentName:   cfg.Name,
		Mode:        string(mode),
		ChannelType: channelType,
		ChannelID:   channelID,
	}); err != nil && !errors.Is(err, platformdb.ErrDuplicate) {
		return fmt.Errorf("creating session in db: %w", err)
	}
	return nil
}

// agentHasSkills reports whether the agent has skills defined inline or on disk.
func (m *Manager) agentHasSkills(cfg config.AgentConfig) bool {
	if len(cfg.Skills) > 0 {
		return true
	}
	skillsDir := filepath.Join(m.agentDefDir(cfg.Name), "skills")
	_, err := os.Stat(skillsDir)
	return err == nil
}

// createInferenceLoop creates a language model and inference loop for the instance.
// Returns nil loop if no provider is configured (test mode).
func (m *Manager) createInferenceLoop(ctx context.Context, loopCfg *inference.LoopConfig, modelSpec models.ModelSpec, apiKey, baseURL string) (*inference.Loop, error) {
	if modelSpec.Provider == "" {
		return nil, nil
	}
	lm, err := provider.CreateLanguageModel(ctx, provider.Type(modelSpec.Provider), apiKey, baseURL, modelSpec.Model)
	if err != nil {
		return nil, fmt.Errorf("creating language model for %q: %w", loopCfg.AgentConfig.Name, err)
	}
	loopCfg.LM = lm
	loop, err := inference.NewLoop(loopCfg)
	if err != nil {
		return nil, fmt.Errorf("creating inference loop for %q: %w", loopCfg.AgentConfig.Name, err)
	}
	return loop, nil
}

// spawnWorker spawns a worker process either locally or on a remote cluster node.
func (m *Manager) spawnWorker(ctx context.Context, cfg config.AgentConfig, nodeID ipc.NodeID, spawnCfg ipc.SpawnConfig, allowedTools map[string]bool, instanceID, sessionID string) (*WorkerHandle, error) {
	isRemote := nodeID != "" && nodeID != ipc.HomeNodeID && m.clusterService != nil
	if isRemote {
		rh, err := m.clusterService.SpawnOnNode(ctx, nodeID, cluster.SpawnRequest{
			InstanceID:     instanceID,
			SessionID:      sessionID,
			AgentName:      cfg.Name,
			EffectiveTools: allowedTools,
			WorkingDir:     ".", // relative to node's HIRO_ROOT
			SessionDir:     filepath.Join("instances", instanceID, "sessions", sessionID),
		})
		if err != nil {
			return nil, fmt.Errorf("spawning agent %q on node %s: %w", cfg.Name, nodeID, err)
		}
		return &WorkerHandle{
			Worker: rh.Worker,
			Kill:   rh.Kill,
			Close:  rh.Close,
			Done:   rh.Done,
		}, nil
	}
	handle, err := m.workerFactory(ctx, spawnCfg)
	if err != nil {
		return nil, fmt.Errorf("spawning agent %q: %w", cfg.Name, err)
	}
	return handle, nil
}

// startInstance creates instance and session directories, spawns a worker process,
// and registers the instance in the manager. nodeID targets a specific cluster
// node ("" or "home" for local execution).
// prepareInstanceDirs creates instance and session directories, seeding state files
// for new instances. Returns the instance dir, session dir, and whether the instance
// dir was newly created.
func (m *Manager) prepareInstanceDirs(instanceID, sessionID string, mode config.AgentMode, displayName, displayDesc, personaBody string, allowedTools, disallowedTools []string) (instDir, sessDir string, dirIsNew bool, err error) {
	instDir = m.instanceDir(instanceID)
	_, statErr := os.Stat(instDir)
	dirIsNew = os.IsNotExist(statErr)
	if err = os.MkdirAll(instDir, fsperm.DirPrivate); err != nil {
		return "", "", false, fmt.Errorf("creating instance dir: %w", err)
	}
	if dirIsNew {
		configPath := m.instanceConfigPath(instanceID)
		if err := seedInstanceFiles(instDir, configPath, mode, displayName, displayDesc, personaBody, allowedTools, disallowedTools); err != nil {
			return "", "", false, err
		}
	}
	sessDir = m.instanceSessionDir(instanceID, sessionID)
	if err := createSessionDirs(sessDir); err != nil {
		return "", "", false, err
	}
	return instDir, sessDir, dirIsNew, nil
}

func (m *Manager) startInstance(ctx context.Context, instanceID, sessionID, channelKey string, cfg config.AgentConfig, parentID string, mode config.AgentMode, nodeID ipc.NodeID, displayName, displayDesc, personaBody string) (string, error) {
	instDir, sessDir, dirIsNew, err := m.prepareInstanceDirs(instanceID, sessionID, mode, displayName, displayDesc, personaBody, cfg.AllowedTools, cfg.DisallowedTools)
	if err != nil {
		return "", err
	}
	if nodeID == "" {
		nodeID = ipc.HomeNodeID
	}
	chType, chID := splitChannelKey(channelKey)
	if err := m.registerInstanceInDB(ctx, instanceID, sessionID, chType, chID, cfg, mode, parentID, nodeID); err != nil {
		return "", err
	}
	// Instance config is the source of truth for tool declarations.
	// Override the agent definition's tools with the instance's config.
	applyInstanceToolConfig(m.instanceConfigPath(instanceID), &cfg)
	effectiveTools, allowLayers, denyRules, err := m.computeEffectiveTools(cfg, parentID)
	if err != nil {
		return "", err
	}
	hasSkills := m.agentHasSkills(cfg)
	allowedTools := buildAllowedToolsMap(effectiveTools, mode, hasSkills)
	spawnCtx := ctx
	if mode.IsPersistent() {
		spawnCtx = m.ctx
	}
	modelSpec, apiKey, baseURL, err := m.resolveModelSpec(cfg.Model)
	if err != nil {
		return "", err
	}

	spawnCfg := m.buildSpawnConfig(instanceID, sessionID, cfg.Name, allowedTools, instDir, sessDir)
	cleanup := makeCleanup(sessDir, instDir, m.instanceConfigPath(instanceID), dirIsNew)
	handle, err := m.spawnWorker(spawnCtx, cfg, nodeID, spawnCfg, allowedTools, instanceID, sessionID)
	if err != nil {
		cleanup()
		return "", err
	}
	if s, ok := handle.Worker.(ipc.SecretEnvSetter); ok {
		s.SetSecretEnvFn(m.SecretEnv)
	}

	notifications := inference.NewNotificationQueue(
		m.logger.With("component", "notifications", "instance_id", instanceID),
	)
	memoryMu := &sync.Mutex{}
	loopCfg := m.buildLoopConfig(instanceID, sessionID, cfg, mode, instDir, sessDir, handle.Worker, allowedTools, allowLayers, denyRules, hasSkills, modelSpec, notifications, memoryMu)
	loop, err := m.createInferenceLoop(spawnCtx, &loopCfg, modelSpec, apiKey, baseURL)
	if err != nil {
		handle.Kill()
		cleanup()
		return "", err
	}

	m.registerAndStartInstance(spawnCtx, buildInstance(instanceID, sessionID, channelKey, cfg, mode, parentID, nodeID, modelSpec.String(), displayName, displayDesc, handle, loop, notifications, memoryMu, effectiveTools, allowLayers, denyRules))
	return instanceID, nil
}

// buildSpawnConfig creates an ipc.SpawnConfig for worker spawning.
func (m *Manager) buildSpawnConfig(instanceID, sessionID, agentName string, allowedTools map[string]bool, instDir, sessDir string) ipc.SpawnConfig {
	// Compute the socket dir that prepareSocketDir will create. This must
	// match exactly so the Landlock RW path covers socket creation.
	sessPrefix := sessionID
	if len(sessPrefix) > ipc.MaxSessionPrefix {
		sessPrefix = sessPrefix[:ipc.MaxSessionPrefix]
	}
	socketDir := filepath.Join(os.TempDir(), fmt.Sprintf("hiro-%s", sessPrefix))

	cfg := ipc.SpawnConfig{
		InstanceID:     instanceID,
		SessionID:      sessionID,
		AgentName:      agentName,
		EffectiveTools: allowedTools,
		WorkingDir:     m.opts.WorkingDir,
		SessionDir:     sessDir,
		NetworkAccess:  allowedTools["Bash"], // Bash tool implies socket access needed
	}

	if m.landlockEnabled {
		cfg.LandlockPaths = m.buildLandlockPaths(instDir, sessDir, socketDir, allowedTools)
	}

	return cfg
}

// buildLandlockPaths computes filesystem access paths for Landlock.
func (m *Manager) buildLandlockPaths(instDir, sessDir, socketDir string, allowedTools map[string]bool) ipc.LandlockPaths {
	agentsDir := filepath.Join(m.rootDir, "agents")
	skillsDir := filepath.Join(m.rootDir, "skills")

	paths := ipc.LandlockPaths{
		ReadWrite: []string{
			instDir,                               // instance dir (persona.md, memory.md, etc.)
			sessDir,                               // session dir (scratch/, tmp/)
			socketDir,                             // gRPC socket dir (under /tmp)
			filepath.Join(m.rootDir, "workspace"), // shared workspace
		},
		ReadOnly: []string{
			"/usr",  // system binaries/libraries
			"/lib",  // shared libraries
			"/etc",  // system config (resolv.conf, etc.)
			"/proc", // /proc/self for Go runtime (os.Executable, net package)
			"/dev",  // /dev/null, /dev/urandom — required by many tools
		},
	}

	// Operators need write access to agents/ and skills/ for creating new
	// agent definitions. Other agents only need read access.
	if allowedTools["CreatePersistentInstance"] {
		paths.ReadWrite = append(paths.ReadWrite, agentsDir, skillsDir)
	} else {
		paths.ReadOnly = append(paths.ReadOnly, agentsDir, skillsDir)
	}

	// mise tool manager paths
	if miseDir := os.Getenv("MISE_DATA_DIR"); miseDir != "" {
		paths.ReadOnly = append(paths.ReadOnly, miseDir)
	} else if _, err := os.Stat("/opt/mise"); err == nil {
		paths.ReadOnly = append(paths.ReadOnly, "/opt/mise")
	}

	return paths
}

// makeCleanup returns a function that removes directories and config on failure.
func makeCleanup(sessDir, instDir, configPath string, dirIsNew bool) func() {
	return func() {
		os.RemoveAll(sessDir)
		if dirIsNew {
			os.RemoveAll(instDir)
			os.Remove(configPath)
		}
	}
}

// buildInstance creates an instance struct with all resolved fields.
// The initial session slot is created from the provided handle/loop.
func buildInstance(instanceID, sessionID, channelKey string, cfg config.AgentConfig, mode config.AgentMode, parentID string, nodeID ipc.NodeID, model, displayName, displayDesc string, handle *WorkerHandle, loop *inference.Loop, notifications *inference.NotificationQueue, memoryMu *sync.Mutex, effectiveTools map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule) *instance {
	slot := &sessionSlot{
		sessionID: sessionID,
		channel:   channelKey,
		worker:    handle.Worker,
		handle:    handle,
		loop:      loop,
		lastUsed:  time.Now(),
	}
	inst := &instance{
		info: InstanceInfo{
			ID:          instanceID,
			Name:        coalesce(displayName, cfg.Name),
			Mode:        mode,
			Description: coalesce(displayDesc, cfg.Description),
			ParentID:    parentID,
			Status:      InstanceStatusRunning,
			Model:       model,
			NodeID:      nodeID,
		},
		agentName:      cfg.Name,
		sessions:       map[string]*sessionSlot{sessionID: slot},
		channelIndex:   map[string]string{channelKey: sessionID},
		notifications:  notifications,
		memoryMu:       memoryMu,
		effectiveTools: effectiveTools,
		allowLayers:    allowLayers,
		denyRules:      denyRules,
		nodeID:         nodeID,
	}
	return inst
}

// coalesce returns override if non-empty, otherwise fallback.
func coalesce(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// buildLoopConfig constructs the inference.LoopConfig struct from instance parameters.
func (m *Manager) buildLoopConfig(instanceID, sessionID string, cfg config.AgentConfig, mode config.AgentMode, instDir, sessDir string, executor ipc.AgentWorker, allowedTools map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule, hasSkills bool, modelSpec models.ModelSpec, notifications *inference.NotificationQueue, memoryMu *sync.Mutex) inference.LoopConfig {
	return inference.LoopConfig{
		InstanceID:     instanceID,
		SessionID:      sessionID,
		AgentConfig:    cfg,
		Mode:           mode,
		WorkingDir:     m.opts.WorkingDir,
		InstanceDir:    instDir,
		MemoryMu:       memoryMu,
		SessionDir:     sessDir,
		AgentDefDir:    m.agentDefDir(cfg.Name),
		SharedSkillDir: m.sharedSkillsDir(),
		Model:          modelSpec.String(),
		Provider:       modelSpec.Provider,
		Executor:       executor,
		PDB:            m.pdb,
		AllowedTools:   allowedTools,
		AllowLayers:    allowLayers,
		DenyRules:      denyRules,
		MaxTurns:       cfg.MaxTurns,
		HasSkills:      hasSkills,
		SecretEnvFn:    m.SecretEnv,
		Notifications:  notifications,
		Logger:         m.logger.With("instance", instanceID, "session", sessionID, "agent", cfg.Name),
		HostManager:    m,
		ContextProviders: []inference.ContextProvider{
			inference.MemoryProvider(instDir),
			inference.TodoProvider(sessDir),
			inference.SecretProvider(m.SecretNames),
			inference.AgentListingProvider(m),
			inference.NodeListingProvider(m),
			inference.SkillProvider(m.agentDefDir(cfg.Name), m.sharedSkillsDir()),
			inference.SubscriptionProvider(m.pdb, instanceID),
		},
		ScheduleCallback: m.scheduler,
		Timezone:         m.timezone,
	}
}

// registerAndStartInstance adds the instance to the manager's registry and starts
// background watchers for all session slots.
func (m *Manager) registerAndStartInstance(ctx context.Context, inst *instance) {
	instanceID := inst.info.ID
	parentID := inst.info.ParentID

	m.mu.Lock()
	m.instances[instanceID] = inst
	if parentID != "" {
		m.children[parentID] = append(m.children[parentID], instanceID)
	}
	m.mu.Unlock()

	// Start watchers for each initial session slot.
	for _, slot := range inst.sessions {
		go m.watchSlotWorker(instanceID, slot.sessionID, slot.handle)
		go m.watchSlotJobCompletions(ctx, slot.worker, inst.notifications, slot.sessionID)
	}

	sessionIDs := make([]string, 0, len(inst.sessions))
	for sid := range inst.sessions {
		sessionIDs = append(sessionIDs, sid)
	}
	m.logger.Info("instance started",
		"id", instanceID,
		"sessions", sessionIDs,
		"name", inst.agentName,
		"mode", inst.info.Mode,
		"parent", parentID,
	)

	// Notify lifecycle hook (e.g. channel manager) after the instance is running.
	if m.lifecycleHook != nil {
		if err := m.lifecycleHook.OnInstanceStart(ctx, instanceID, m.instanceConfigPath(instanceID)); err != nil {
			m.logger.Warn("lifecycle hook OnInstanceStart failed", "instance", instanceID, "error", err)
		}
	}
}
