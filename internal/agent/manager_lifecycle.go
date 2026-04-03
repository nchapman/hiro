package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
	"github.com/nchapman/hiro/internal/uidpool"
)

// CreateInstance loads an agent definition by name and starts an instance in the
// given mode. The ctx parameter is used only for config loading and worker
// spawning — persistent/coordinator instances use the manager's lifetime context.
// parentInstanceID tracks lineage; pass "" for top-level instances.
// mode is a string to satisfy the ipc.HostManager interface boundary; it must
// be one of "persistent" or "ephemeral".
// displayName and displayDesc override the agent definition name/description
// in persona.md frontmatter (pass "" to use defaults).
func (m *Manager) CreateInstance(ctx context.Context, name, parentInstanceID, mode string, nodeID ipc.NodeID, displayName, displayDesc string) (string, error) {
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

	instanceID := uuid.Must(uuid.NewV7()).String()
	sessionID := uuid.Must(uuid.NewV7()).String()
	m.logger.Info("creating instance", "instance_id", instanceID, "agent", name, "mode", mode)
	id, err2 := m.startInstance(ctx, instanceID, sessionID, cfg, parentInstanceID, agentMode, nodeID, displayName, displayDesc)
	if err2 != nil {
		m.logger.Error("instance creation failed", "instance_id", instanceID, "agent", name, "error", err2)
	}
	return id, err2
}

// resolveSupplementaryGroups resolves the supplementary Unix groups for an agent,
// intersecting declared groups with the parent's groups. Returns nil if no UID
// pool is configured. This is the single source of truth for group resolution —
// used by both startInstance (running) and RestoreInstances (stopped).
func (m *Manager) resolveSupplementaryGroups(cfg config.AgentConfig, parentID string) []uint32 {
	if m.uidPool == nil {
		return nil
	}
	parentGroups := m.parentGroupSet(parentID)
	var groups []uint32
	for _, g := range cfg.Groups {
		groupGID := m.uidPool.GroupGID(g)
		if groupGID == 0 {
			m.logger.Warn("agent declared unknown group, ignoring",
				"agent", cfg.Name, "group", g)
			continue
		}
		if parentGroups != nil && !parentGroups[groupGID] {
			m.logger.Warn("agent declared group not held by parent, ignoring",
				"agent", cfg.Name, "group", g)
			continue
		}
		groups = append(groups, groupGID)
	}
	return groups
}

// parentGroupSet returns the set of supplementary GIDs held by the parent instance.
// Returns nil if there is no parent (root instance — no restriction).
// Returns an empty non-nil map if the parent exists but has no groups, or if the
// parent ID is specified but not found (fail-closed: deny all supplementary groups).
func (m *Manager) parentGroupSet(parentID string) map[uint32]bool {
	if parentID == "" {
		return nil // root instance — no restriction
	}
	m.mu.RLock()
	parent, ok := m.instances[parentID]
	m.mu.RUnlock()
	if !ok {
		// Parent specified but not found — deny all supplementary groups.
		return map[uint32]bool{}
	}
	set := make(map[uint32]bool, len(parent.groups))
	for _, g := range parent.groups {
		set[g] = true
	}
	return set
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

	instanceID := uuid.Must(uuid.NewV7()).String()
	sessionID := uuid.Must(uuid.NewV7()).String()
	m.logger.Info("spawning ephemeral", "instance_id", instanceID, "agent", agentName)
	instID, err := m.startInstance(ctx, instanceID, sessionID, cfg, parentInstanceID, config.ModeEphemeral, nodeID, "", "")
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

		// Always delete instance dir and DB record regardless of mode.
		os.RemoveAll(m.instanceDir(id))
		if m.pdb != nil {
			_ = m.pdb.DeleteInstance(context.Background(), id)
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

// seedInstanceFiles creates persona.md and memory.md in a new instance directory.
func seedInstanceFiles(instDir string, mode config.AgentMode, displayName, displayDesc string) error {
	if mode.IsPersistent() && (displayName != "" || displayDesc != "") {
		if err := config.WritePersonaFile(instDir, displayName, displayDesc, ""); err != nil {
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
func (m *Manager) registerInstanceInDB(ctx context.Context, instanceID, sessionID string, cfg config.AgentConfig, mode config.AgentMode, parentID string, nodeID ipc.NodeID) error {
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
		ID:         sessionID,
		InstanceID: instanceID,
		AgentName:  cfg.Name,
		Mode:       string(mode),
	}); err != nil && !errors.Is(err, platformdb.ErrDuplicate) {
		return fmt.Errorf("creating session in db: %w", err)
	}
	return nil
}

// acquireUIDAndChown acquires a UID from the pool and chowns the instance directory.
// Returns uid, gid (both zero if no pool configured).
func (m *Manager) acquireUIDAndChown(instanceID, instDir string) (uint32, uint32, error) {
	if m.uidPool == nil {
		return 0, 0, nil
	}
	uid, gid, err := m.uidPool.Acquire(instanceID)
	if err != nil {
		return 0, 0, fmt.Errorf("acquiring UID: %w", err)
	}
	// Transfer ownership of the instance dir (and all contents) to the agent user.
	if err := filepath.WalkDir(instDir, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chown(path, int(uid), int(gid)) //nolint:gosec // G122: controlled instance directory, no symlink risk
	}); err != nil {
		if os.IsPermission(err) {
			m.logger.Warn("cannot chown instance dir (not root); file isolation degraded",
				"instance", instanceID, "uid", uid)
		} else {
			m.uidPool.Release(instanceID)
			return 0, 0, fmt.Errorf("chowning instance dir: %w", err)
		}
	}
	return uid, gid, nil
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
func (m *Manager) createInferenceLoop(ctx context.Context, loopCfg inference.LoopConfig, modelSpec models.ModelSpec, apiKey, baseURL string) (*inference.Loop, error) {
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
func (m *Manager) prepareInstanceDirs(instanceID, sessionID string, mode config.AgentMode, displayName, displayDesc string) (instDir, sessDir string, dirIsNew bool, err error) {
	instDir = m.instanceDir(instanceID)
	_, statErr := os.Stat(instDir)
	dirIsNew = os.IsNotExist(statErr)
	if err = os.MkdirAll(instDir, fsperm.DirPrivate); err != nil {
		return "", "", false, fmt.Errorf("creating instance dir: %w", err)
	}
	if dirIsNew {
		if err := seedInstanceFiles(instDir, mode, displayName, displayDesc); err != nil {
			return "", "", false, err
		}
	}
	sessDir = m.instanceSessionDir(instanceID, sessionID)
	if err := createSessionDirs(sessDir); err != nil {
		return "", "", false, err
	}
	return instDir, sessDir, dirIsNew, nil
}

func (m *Manager) startInstance(ctx context.Context, instanceID, sessionID string, cfg config.AgentConfig, parentID string, mode config.AgentMode, nodeID ipc.NodeID, displayName, displayDesc string) (string, error) {
	instDir, sessDir, dirIsNew, err := m.prepareInstanceDirs(instanceID, sessionID, mode, displayName, displayDesc)
	if err != nil {
		return "", err
	}
	resolvedNodeID := nodeID
	if resolvedNodeID == "" {
		resolvedNodeID = ipc.HomeNodeID
	}
	if err := m.registerInstanceInDB(ctx, instanceID, sessionID, cfg, mode, parentID, resolvedNodeID); err != nil {
		return "", err
	}
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
	supplementaryGroups := m.resolveSupplementaryGroups(cfg, parentID)
	uid, gid, err := m.acquireUIDAndChown(instanceID, instDir)
	if err != nil {
		return "", err
	}
	modelSpec, apiKey, baseURL, err := m.resolveModelSpec(cfg.Model)
	if err != nil {
		return "", err
	}

	spawnCfg := m.buildSpawnConfig(instanceID, sessionID, cfg.Name, allowedTools, sessDir, uid, gid, supplementaryGroups)
	cleanup := makeCleanup(sessDir, instDir, dirIsNew, m.uidPool, instanceID)
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
	loopCfg := m.buildLoopConfig(instanceID, sessionID, cfg, mode, instDir, sessDir, handle.Worker, allowedTools, allowLayers, denyRules, hasSkills, modelSpec, notifications)
	loop, err := m.createInferenceLoop(spawnCtx, loopCfg, modelSpec, apiKey, baseURL)
	if err != nil {
		handle.Kill()
		cleanup()
		return "", err
	}

	m.registerAndStartInstance(spawnCtx, buildInstance(instanceID, sessionID, cfg, mode, parentID, resolvedNodeID, modelSpec.String(), displayName, displayDesc, handle, loop, notifications, effectiveTools, allowLayers, denyRules, uid, gid, supplementaryGroups))
	return instanceID, nil
}

// buildSpawnConfig creates an ipc.SpawnConfig for worker spawning.
func (m *Manager) buildSpawnConfig(instanceID, sessionID, agentName string, allowedTools map[string]bool, sessDir string, uid, gid uint32, supplementaryGroups []uint32) ipc.SpawnConfig {
	return ipc.SpawnConfig{
		InstanceID:     instanceID,
		SessionID:      sessionID,
		AgentName:      agentName,
		EffectiveTools: allowedTools,
		WorkingDir:     m.opts.WorkingDir,
		SessionDir:     sessDir,
		AgentSocket:    filepath.Join(os.TempDir(), fmt.Sprintf("hiro-agent-%s.sock", sessionID)),
		UID:            uid,
		GID:            gid,
		Groups:         append([]uint32{gid}, supplementaryGroups...),
	}
}

// makeCleanup returns a function that removes directories and releases UID on failure.
func makeCleanup(sessDir, instDir string, dirIsNew bool, pool *uidpool.Pool, instanceID string) func() {
	return func() {
		os.RemoveAll(sessDir)
		if dirIsNew {
			os.RemoveAll(instDir)
		}
		if pool != nil {
			pool.Release(instanceID)
		}
	}
}

// buildInstance creates an instance struct with all resolved fields.
func buildInstance(instanceID, sessionID string, cfg config.AgentConfig, mode config.AgentMode, parentID string, nodeID ipc.NodeID, model, displayName, displayDesc string, handle *WorkerHandle, loop *inference.Loop, notifications *inference.NotificationQueue, effectiveTools map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule, uid, gid uint32, groups []uint32) *instance {
	return &instance{
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
		activeSession:  sessionID,
		worker:         handle.Worker,
		handle:         handle,
		loop:           loop,
		notifications:  notifications,
		effectiveTools: effectiveTools,
		allowLayers:    allowLayers,
		denyRules:      denyRules,
		uid:            uid,
		gid:            gid,
		groups:         groups,
		nodeID:         nodeID,
	}
}

// coalesce returns override if non-empty, otherwise fallback.
func coalesce(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// buildLoopConfig constructs the inference.LoopConfig struct from instance parameters.
func (m *Manager) buildLoopConfig(instanceID, sessionID string, cfg config.AgentConfig, mode config.AgentMode, instDir, sessDir string, executor ipc.AgentWorker, allowedTools map[string]bool, allowLayers [][]toolrules.Rule, denyRules []toolrules.Rule, hasSkills bool, modelSpec models.ModelSpec, notifications *inference.NotificationQueue) inference.LoopConfig {
	return inference.LoopConfig{
		InstanceID:     instanceID,
		SessionID:      sessionID,
		AgentConfig:    cfg,
		Mode:           mode,
		WorkingDir:     m.opts.WorkingDir,
		InstanceDir:    instDir,
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
		SecretNamesFn:  m.SecretNames,
		SecretEnvFn:    m.SecretEnv,
		Notifications:  notifications,
		Logger:         m.logger.With("instance", instanceID, "session", sessionID, "agent", cfg.Name),
		HostManager:    m,
	}
}

// registerAndStartInstance adds the instance to the manager's registry and starts
// its background watchers (death-watcher and job completion).
func (m *Manager) registerAndStartInstance(ctx context.Context, inst *instance) {
	instanceID := inst.info.ID
	parentID := inst.info.ParentID

	m.mu.Lock()
	m.instances[instanceID] = inst
	if parentID != "" {
		m.children[parentID] = append(m.children[parentID], instanceID)
	}
	m.mu.Unlock()

	// Start death-watcher goroutine for unexpected process exits.
	go m.watchWorker(instanceID, inst.handle.Done)

	// Start background job completion watcher to push notifications
	// when background bash tasks finish on this worker.
	go m.watchJobCompletions(ctx, inst.worker, inst.notifications)

	m.logger.Info("instance started",
		"id", instanceID,
		"session", inst.activeSession,
		"name", inst.agentName,
		"mode", inst.info.Mode,
		"parent", parentID,
	)
}
