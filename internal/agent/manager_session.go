package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/inference"
	"github.com/nchapman/hiro/internal/ipc"
	"github.com/nchapman/hiro/internal/models"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
	"github.com/nchapman/hiro/internal/provider"
	"github.com/nchapman/hiro/internal/watcher"
)

var validEfforts = map[string]bool{
	"": true, "on": true, "low": true, "medium": true, "high": true, "max": true,
	"minimal": true, "xhigh": true, // OpenAI/OpenRouter levels
}

func validReasoningEffort(effort string) bool {
	return validEfforts[effort]
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
		spec, apiKey, baseURL, err := m.resolveModelSpec(model)
		if err != nil {
			return err
		}

		lm, err := provider.CreateLanguageModel(ctx, provider.Type(spec.Provider), apiKey, baseURL, spec.Model)
		if err != nil {
			return fmt.Errorf("creating language model %q: %w", model, err)
		}
		inst.loop.UpdateModel(lm, spec.String(), spec.Provider)
		inst.info.Model = spec.String()
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
		if err := m.pdb.UpdateInstanceConfig(ctx, instanceID, cfg); err != nil {
			m.logger.Warn("failed to persist instance config", "instance", instanceID, "error", err)
		}
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

	name := inst.agentName
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
		if sess, ok, sessErr := m.pdb.LatestSessionByInstance(ctx, instanceID); sessErr != nil {
			m.logger.Warn("failed to query latest session", "instance", instanceID, "error", sessErr)
		} else if ok {
			sessionID = sess.ID
		}
	}
	if sessionID == "" {
		sessionID = uuid.Must(uuid.NewV7()).String()
	}
	if _, err = m.startInstance(ctx, instanceID, sessionID, cfg, parentID, mode, inst.nodeID, "", "", ""); err != nil {
		// Re-register as stopped so the instance remains visible.
		m.reregisterStopped(instanceID, inst)
		return err
	}

	// Clear the stopped flag so the instance starts on next server restart.
	m.setInstanceStatus(instanceID, string(InstanceStatusRunning))

	// Resume cron subscriptions for this instance.
	if m.scheduler != nil {
		m.scheduler.ResumeInstance(ctx, instanceID)
	}
	return nil
}

// NewSession ends the current session and starts a new one within the same instance.
// This is the /clear handler — persona and memory persist, messages and todos reset.
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

	// Capture old handle so we can shut it down after the new spawn.
	// Nil it out before spawning the new worker — watchWorker uses
	// pointer identity (inst.handle != handle) to detect stale exits.
	oldHandle := inst.handle
	inst.handle = nil
	oldSession := inst.activeSession

	m.markSessionStopped(oldSession)

	// Create new session directory.
	newSessionID := uuid.Must(uuid.NewV7()).String()
	sessDir := m.instanceSessionDir(instanceID, newSessionID)
	if err := createSessionDirs(sessDir); err != nil {
		return "", err
	}

	if err := m.registerSessionInDB(instanceID, newSessionID, inst.agentName, inst.info.Mode); err != nil {
		return "", err
	}

	chownDir(sessDir, inst.uid, inst.gid, m.logger, "session", newSessionID)

	sc, err := m.resolveSessionConfig(inst)
	if err != nil {
		return "", err
	}

	spawnCfg := m.buildSpawnConfig(instanceID, newSessionID, sc.agentConfig.Name, sc.allowedTools, sessDir, inst.uid, inst.gid, inst.groups)

	// Shut down old worker concurrently while spawning the new one.
	shutdownDone := m.shutdownOldWorkerAsync(oldHandle)

	handle, loop, err := m.spawnSessionWorkerAndLoop(m.ctx, inst, sc.agentConfig, spawnCfg, sc.allowedTools, sc.modelSpec, sc.apiKey, sc.baseURL, instanceID, newSessionID, sessDir)
	if err != nil {
		return m.failNewSession(shutdownDone, instanceID, newSessionID, sessDir, inst, err)
	}

	<-shutdownDone

	inst.activeSession = newSessionID
	inst.worker = handle.Worker
	inst.handle = handle
	inst.loop = loop

	go m.watchWorker(instanceID, handle)
	go m.watchJobCompletions(m.ctx, handle.Worker, inst.notifications)

	m.logger.Info("new session created",
		"instance", instanceID, "session", newSessionID, "agent", inst.agentName)

	return newSessionID, nil
}

// registerSessionInDB creates a session record in the platform database.
func (m *Manager) registerSessionInDB(instanceID, sessionID, agentName string, mode config.AgentMode) error {
	if m.pdb == nil {
		return nil
	}
	return m.pdb.CreateSession(context.Background(), platformdb.Session{
		ID:         sessionID,
		InstanceID: instanceID,
		AgentName:  agentName,
		Mode:       string(mode),
	})
}

// failNewSession marks an instance as stopped after a failed session spawn.
// inst.mu must be held by the caller.
func (m *Manager) failNewSession(shutdownDone <-chan struct{}, instanceID, sessionID, sessDir string, inst *instance, err error) (string, error) {
	<-shutdownDone
	inst.info.Status = InstanceStatusStopped
	m.setInstanceStatus(instanceID, string(InstanceStatusStopped))
	os.RemoveAll(sessDir)
	if m.pdb != nil {
		if err := m.pdb.DeleteSession(context.Background(), sessionID); err != nil {
			m.logger.Warn("failed to delete session from DB", "session", sessionID, "error", err)
		}
	}
	return "", err
}

// markSessionStopped updates the old session status in the DB. Best-effort.
func (m *Manager) markSessionStopped(sessionID string) {
	if m.pdb != nil && sessionID != "" {
		if err := m.pdb.UpdateSessionStatus(context.Background(), sessionID, "stopped"); err != nil {
			m.logger.Warn("failed to mark old session as stopped", "session", sessionID, "error", err)
		}
	}
}

// chownDir recursively chowns a directory to the given uid/gid.
// No-op if uid is 0 (isolation not enabled).
func chownDir(dir string, uid, gid uint32, logger interface{ Warn(string, ...any) }, label, id string) {
	if uid == 0 {
		return
	}
	if err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chown(path, int(uid), int(gid)) //nolint:gosec // G122: controlled directory, no symlink risk
	}); err != nil {
		logger.Warn("failed to chown dir", label, id, "error", err)
	}
}

// sessionConfig holds the resolved configuration for starting a new session.
type sessionConfig struct {
	agentConfig  config.AgentConfig
	allowedTools map[string]bool
	modelSpec    models.ModelSpec
	apiKey       string
	baseURL      string
}

// resolveSessionConfig reloads the agent config and resolves tools and provider
// for a new session. Updates inst.effectiveTools/allowLayers/denyRules in place.
func (m *Manager) resolveSessionConfig(inst *instance) (sessionConfig, error) {
	cfg, err := config.LoadAgentDir(m.agentDefDir(inst.agentName))
	if err != nil {
		return sessionConfig{}, fmt.Errorf("loading agent %q: %w", inst.agentName, err)
	}

	modelSpec, apiKey, baseURL, err := m.resolveModelSpec(cfg.Model)
	if err != nil {
		return sessionConfig{}, err
	}

	hasSkills := m.agentHasSkills(cfg)
	// Recompute effective tools from current config (agent.md + CP + parent).
	// This picks up any permission changes since the instance was created.
	effectiveTools, allowLayers, denyRules, err := m.computeEffectiveTools(cfg, inst.info.ParentID)
	if err != nil {
		return sessionConfig{}, fmt.Errorf("computing effective tools: %w", err)
	}
	inst.effectiveTools = effectiveTools
	inst.allowLayers = allowLayers
	inst.denyRules = denyRules
	allowedTools := buildAllowedToolsMap(effectiveTools, inst.info.Mode, hasSkills)

	return sessionConfig{
		agentConfig:  cfg,
		allowedTools: allowedTools,
		modelSpec:    modelSpec,
		apiKey:       apiKey,
		baseURL:      baseURL,
	}, nil
}

// shutdownOldWorkerAsync shuts down the old worker handle in a goroutine.
// Returns a channel that is closed when shutdown completes.
func (m *Manager) shutdownOldWorkerAsync(oldHandle *WorkerHandle) <-chan struct{} {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		m.shutdownHandle(oldHandle)
		if oldHandle != nil {
			// Close directly (not cleanupWorker) — the UID is retained
			// and reused by the new session for this same instance.
			oldHandle.Close()
		}
	}()
	return shutdownDone
}

// spawnSessionWorkerAndLoop creates a new worker and inference loop for a session.
// Returns the handle and loop, or an error.
func (m *Manager) spawnSessionWorkerAndLoop(ctx context.Context, inst *instance, cfg config.AgentConfig, spawnCfg ipc.SpawnConfig, allowedTools map[string]bool, modelSpec models.ModelSpec, apiKey, baseURL, instanceID, sessionID, sessDir string) (*WorkerHandle, *inference.Loop, error) {
	handle, err := m.workerFactory(ctx, spawnCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("spawning agent %q: %w", cfg.Name, err)
	}
	if s, ok := handle.Worker.(ipc.SecretEnvSetter); ok {
		s.SetSecretEnvFn(m.SecretEnv)
	}

	hasSkills := len(cfg.Skills) > 0 || m.agentHasSkills(cfg)
	loopCfg := m.buildLoopConfig(instanceID, sessionID, cfg, inst.info.Mode,
		m.instanceDir(instanceID), sessDir, handle.Worker, allowedTools,
		inst.allowLayers, inst.denyRules, hasSkills, modelSpec, inst.notifications)

	loop, err := m.createInferenceLoop(ctx, &loopCfg, modelSpec, apiKey, baseURL)
	if err != nil {
		handle.Kill()
		handle.Close()
		return nil, nil, err
	}

	return handle, loop, nil
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

// configPushContext holds resolved config for pushing updates to running instances.
type configPushContext struct {
	cfg       config.AgentConfig
	modelSpec models.ModelSpec
	apiKey    string
	baseURL   string
	model     string
	hasSkills bool
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

	modelSpec, apiKey, baseURL, err := m.resolveModelSpec(cfg.Model)
	if err != nil {
		m.logger.Warn("failed to resolve model for config push",
			"agent", agentName, "error", err)
		return
	}

	pc := configPushContext{
		cfg:       cfg,
		modelSpec: modelSpec,
		apiKey:    apiKey,
		baseURL:   baseURL,
		model:     modelSpec.String(),
		hasSkills: m.agentHasSkills(cfg),
	}

	type pushTarget struct {
		id       string
		parentID string
		mode     config.AgentMode
	}

	m.mu.RLock()
	var targets []pushTarget
	for id, inst := range m.instances {
		if inst.agentName == agentName && inst.info.Status == InstanceStatusRunning {
			targets = append(targets, pushTarget{id: id, parentID: inst.info.ParentID, mode: inst.info.Mode})
		}
	}
	m.mu.RUnlock()

	for _, t := range targets {
		m.pushConfigToInstance(t.id, t.parentID, t.mode, &pc)
	}
}

// pushConfigToInstance applies a resolved config update to a single running instance.
func (m *Manager) pushConfigToInstance(instanceID, parentID string, mode config.AgentMode, pc *configPushContext) {
	inst := m.getInstance(instanceID)
	if inst == nil {
		return // removed between snapshot and push
	}

	inst.mu.Lock()
	defer inst.mu.Unlock()

	if inst.info.Status == InstanceStatusStopped {
		return
	}

	if inst.loop != nil {
		m.pushToolsAndModel(inst, instanceID, parentID, mode, pc)
	}

	inst.info.Description = pc.cfg.Description
	m.logger.Info("pushed config update to instance",
		"agent", pc.cfg.Name, "instance", instanceID, "model", pc.model)
}

// pushToolsAndModel updates tool rules and model on an instance's inference loop.
// Caller must hold inst.mu.
func (m *Manager) pushToolsAndModel(inst *instance, instanceID, parentID string, mode config.AgentMode, pc *configPushContext) {
	// Recompute effective tools from updated config.
	effectiveTools, allowLayers, denyRules, err := m.computeEffectiveTools(pc.cfg, parentID)
	if err != nil {
		m.logger.Warn("failed to recompute tools for config push",
			"agent", pc.cfg.Name, "instance", instanceID, "error", err)
	} else {
		allowedTools := buildAllowedToolsMap(effectiveTools, mode, pc.hasSkills)
		inst.effectiveTools = effectiveTools
		inst.allowLayers = allowLayers
		inst.denyRules = denyRules
		inst.loop.UpdateToolRules(allowedTools, allowLayers, denyRules)
	}

	// Model switch.
	if pc.model == inst.info.Model {
		return
	}
	const modelSwitchTimeout = 10 * time.Second
	pushCtx, pushCancel := context.WithTimeout(context.Background(), modelSwitchTimeout)
	lm, lmErr := provider.CreateLanguageModel(pushCtx, provider.Type(pc.modelSpec.Provider), pc.apiKey, pc.baseURL, pc.modelSpec.Model)
	pushCancel()
	if lmErr != nil {
		m.logger.Warn("failed to create language model for config push",
			"agent", pc.cfg.Name, "model", pc.model, "error", lmErr)
		return
	}
	inst.loop.UpdateModel(lm, pc.model, pc.modelSpec.Provider)
	inst.info.Model = pc.model
}

// PushConfigUpdateAll recomputes and pushes config to all running instances.
func (m *Manager) PushConfigUpdateAll() {
	m.mu.RLock()
	names := make(map[string]bool)
	for _, inst := range m.instances {
		if inst.info.Status == InstanceStatusRunning {
			names[inst.agentName] = true
		}
	}
	m.mu.RUnlock()

	for name := range names {
		m.pushConfigUpdate(name)
	}
}
