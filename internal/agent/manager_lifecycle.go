package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/nchapman/hiro/internal/cluster"
	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/provider"
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
	m.StopInstance(instID)

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
			m.pdb.DeleteInstance(id)
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

// startInstance creates instance and session directories, spawns a worker process,
// and registers the instance in the manager. nodeID targets a specific cluster
// node ("" or "home" for local execution).
func (m *Manager) startInstance(ctx context.Context, instanceID, sessionID string, cfg config.AgentConfig, parentID string, mode config.AgentMode, nodeID ipc.NodeID, displayName, displayDesc string) (string, error) {
	// Create instance directory with instance-level state.
	instDir := m.instanceDir(instanceID)
	_, statErr := os.Stat(instDir)
	dirIsNew := os.IsNotExist(statErr)
	if err := os.MkdirAll(instDir, 0700); err != nil {
		return "", fmt.Errorf("creating instance dir: %w", err)
	}

	// Seed instance-level state files so agents can discover them.
	if dirIsNew {
		if mode.IsPersistent() && (displayName != "" || displayDesc != "") {
			// Seed persona.md with name/description frontmatter.
			if err := config.WritePersonaFile(instDir, displayName, displayDesc, ""); err != nil {
				return "", fmt.Errorf("creating persona.md: %w", err)
			}
		} else {
			// Seed empty persona.md.
			if err := os.WriteFile(filepath.Join(instDir, "persona.md"), nil, 0600); err != nil {
				return "", fmt.Errorf("creating persona.md: %w", err)
			}
		}
		// Seed empty memory.md.
		memPath := filepath.Join(instDir, "memory.md")
		if err := os.WriteFile(memPath, nil, 0600); err != nil {
			return "", fmt.Errorf("creating memory.md: %w", err)
		}
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
	effectiveTools, allowLayers, denyRules, err := m.computeEffectiveTools(cfg, parentID)
	if err != nil {
		return "", err
	}
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
		// Add supplementary groups declared in the agent definition.
		for _, g := range cfg.Groups {
			if groupGID := m.uidPool.GroupGID(g); groupGID != 0 {
				groups = append(groups, groupGID)
			} else {
				m.logger.Warn("agent declared unknown group, ignoring",
					"agent", cfg.Name, "group", g)
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
	modelSpec, apiKey, baseURL, err := m.resolveModelSpec(cfg.Model)
	if err != nil {
		return "", err
	}

	spawnCfg := ipc.SpawnConfig{
		InstanceID:     instanceID,
		SessionID:      sessionID,
		AgentName:      cfg.Name,
		EffectiveTools: allowedTools,
		WorkingDir:     m.opts.WorkingDir,
		SessionDir:     sessDir,
		AgentSocket:    filepath.Join(os.TempDir(), fmt.Sprintf("hiro-agent-%s.sock", sessionID)),
		UID:            uid,
		GID:            gid,
		Groups:         groups,
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

	// Spawn the worker — either locally or on a remote node.
	isRemote := nodeID != "" && nodeID != ipc.HomeNodeID && m.clusterService != nil
	var handle *WorkerHandle

	if isRemote {
		rh, err := m.clusterService.SpawnOnNode(spawnCtx, nodeID, cluster.SpawnRequest{
			InstanceID:     instanceID,
			SessionID:      sessionID,
			AgentName:      cfg.Name,
			EffectiveTools: allowedTools,
			WorkingDir:     ".", // relative to node's HIRO_ROOT
			SessionDir:     filepath.Join("instances", instanceID, "sessions", sessionID),
		})
		if err != nil {
			cleanup()
			return "", fmt.Errorf("spawning agent %q on node %s: %w", cfg.Name, nodeID, err)
		}
		handle = &WorkerHandle{
			Worker: rh.Worker,
			Kill:   rh.Kill,
			Close:  rh.Close,
			Done:   rh.Done,
		}
	} else {
		var err error
		handle, err = m.workerFactory(spawnCtx, spawnCfg)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("spawning agent %q: %w", cfg.Name, err)
		}
	}

	// Inject secret env provider so bash commands in the worker can access secrets.
	if s, ok := handle.Worker.(ipc.SecretEnvSetter); ok {
		s.SetSecretEnvFn(m.SecretEnv)
	}

	// Create the notification queue (instance-level, survives loop recreation).
	notifications := inference.NewNotificationQueue(
		m.logger.With("component", "notifications", "instance_id", instanceID),
	)

	// Create the inference loop (skipped if no provider — test mode).
	var loop *inference.Loop
	if modelSpec.Provider != "" {
		lm, err := provider.CreateLanguageModel(spawnCtx, provider.Type(modelSpec.Provider), apiKey, baseURL, modelSpec.Model)
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
			Model:          modelSpec.String(),
			Provider:       modelSpec.Provider,
			Executor:       handle.Worker,
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
		})
		if err != nil {
			handle.Kill()
			cleanup()
			return "", fmt.Errorf("creating inference loop for %q: %w", cfg.Name, err)
		}
	}

	resolvedNodeID := nodeID
	if resolvedNodeID == "" {
		resolvedNodeID = ipc.HomeNodeID
	}

	// Resolve display name/description: persona frontmatter overrides agent definition.
	resolvedName := cfg.Name
	if displayName != "" {
		resolvedName = displayName
	}
	resolvedDesc := cfg.Description
	if displayDesc != "" {
		resolvedDesc = displayDesc
	}

	inst := &instance{
		info: InstanceInfo{
			ID:          instanceID,
			Name:        resolvedName,
			Mode:        mode,
			Description: resolvedDesc,
			ParentID:    parentID,
			Status:      InstanceStatusRunning,
			Model:       modelSpec.String(),
			NodeID:      resolvedNodeID,
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
		nodeID:         resolvedNodeID,
	}

	m.mu.Lock()
	m.instances[instanceID] = inst
	if parentID != "" {
		m.children[parentID] = append(m.children[parentID], instanceID)
	}
	m.mu.Unlock()

	// Start death-watcher goroutine for unexpected process exits.
	go m.watchWorker(instanceID, handle.Done)

	// Start background job completion watcher to push notifications
	// when background bash tasks finish on this worker.
	go m.watchJobCompletions(spawnCtx, handle.Worker, notifications)

	m.logger.Info("instance started",
		"id", instanceID,
		"session", sessionID,
		"name", cfg.Name,
		"mode", mode,
		"parent", parentID,
	)

	return instanceID, nil
}
