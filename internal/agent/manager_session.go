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
		// Find which configured provider owns this model.
		providerName, apiKey, baseURL, err := m.resolveProviderForModel(model)
		if err != nil {
			return err
		}

		lm, err := provider.CreateLanguageModel(ctx, provider.Type(providerName), apiKey, baseURL, model)
		if err != nil {
			return fmt.Errorf("creating language model %q: %w", model, err)
		}
		inst.loop.UpdateModel(lm, model, providerName)
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
		if sess, ok, sessErr := m.pdb.LatestSessionByInstance(instanceID); sessErr != nil {
			m.logger.Warn("failed to query latest session", "instance", instanceID, "error", sessErr)
		} else if ok {
			sessionID = sess.ID
		}
	}
	if sessionID == "" {
		sessionID = uuid.Must(uuid.NewV7()).String()
	}
	if _, err = m.startInstance(ctx, instanceID, sessionID, cfg, parentID, mode, inst.nodeID); err != nil {
		// Re-register as stopped so the instance remains visible.
		m.reregisterStopped(instanceID, inst)
		return err
	}

	// Clear the stopped flag so the instance starts on next server restart.
	m.setInstanceStatus(instanceID, "running")
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

	// Capture old handle so we can shut it down concurrently with the new spawn.
	// Nil it out so the old watchWorker goroutine bails instead of tearing down
	// the instance.
	oldHandle := inst.handle
	oldSession := inst.activeSession

	// Mark the old session as stopped in DB.
	if m.pdb != nil && oldSession != "" {
		m.pdb.UpdateSessionStatus(oldSession, "stopped")
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
		if err := filepath.WalkDir(sessDir, func(path string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			return os.Chown(path, int(inst.uid), int(inst.gid))
		}); err != nil {
			m.logger.Warn("failed to chown session dir", "session", newSessionID, "error", err)
		}
	}

	// Reload agent config and resolve provider.
	cfg, err := config.LoadAgentDir(m.agentDefDir(inst.info.Name))
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", inst.info.Name, err)
	}

	providerName, apiKey, baseURL, err := m.resolveProvider()
	if err != nil {
		return "", err
	}
	model := m.resolveModel()

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
		AgentSocket:    filepath.Join(os.TempDir(), fmt.Sprintf("hiro-agent-%s.sock", newSessionID)),
		UID:            inst.uid,
		GID:            inst.gid,
		Groups:         inst.groups,
	}

	// Shut down old worker concurrently while spawning the new one.
	// Different socket paths and session dirs, so no resource conflicts.
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

	// failStopped marks the instance as stopped on spawn/loop failure.
	failStopped := func(err error) (string, error) {
		<-shutdownDone // wait for old worker cleanup before marking stopped
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
	if s, ok := handle.Worker.(ipc.SecretEnvSetter); ok {
		s.SetSecretEnvFn(m.SecretEnv)
	}

	// Create new inference loop.
	var loop *inference.Loop
	if providerName != "" {
		lm, err := provider.CreateLanguageModel(spawnCtx, provider.Type(providerName), apiKey, baseURL, model)
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
			Model:          model,
			Provider:       providerName,
			Executor:       handle.Worker,
			PDB:            m.pdb,
			AllowedTools:   allowedTools,
			HasSkills:      hasSkills,
			SecretNamesFn:  m.SecretNames,
			SecretEnvFn:    m.SecretEnv,
			Notifications:  inst.notifications,
			Logger:         m.logger.With("instance", instanceID, "session", newSessionID, "agent", cfg.Name),
			HostManager:    m,
			InstanceMode:     inst.info.Mode,
		})
		if err != nil {
			handle.Kill()
			return failStopped(fmt.Errorf("creating inference loop for %q: %w", cfg.Name, err))
		}
	}

	// Wait for old worker to finish before swapping, so the old watchWorker
	// goroutine sees nil handle and bails cleanly.
	<-shutdownDone

	// Swap — inst.loop goes directly from old to new.
	inst.activeSession = newSessionID
	inst.worker = handle.Worker
	inst.handle = handle
	inst.loop = loop

	go m.watchWorker(instanceID, handle.Done)
	go m.watchJobCompletions(spawnCtx, handle.Worker, inst.notifications)

	m.logger.Info("new session created",
		"instance", instanceID,
		"session", newSessionID,
		"agent", inst.info.Name,
	)

	return newSessionID, nil
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

	providerName, apiKey, baseURL, err := m.resolveProvider()
	if err != nil {
		m.logger.Warn("failed to resolve provider for config push",
			"agent", agentName, "error", err)
		return
	}
	model := m.resolveModel()

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
		currentModel string
	}

	m.mu.RLock()
	var targets []pushTarget
	for id, inst := range m.instances {
		if inst.info.Name == agentName && inst.info.Status == InstanceStatusRunning {
			targets = append(targets, pushTarget{id: id, parentID: inst.info.ParentID, mode: inst.info.Mode, currentModel: inst.info.Model})
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
			pushCtx, pushCancel := context.WithTimeout(context.Background(), 10*time.Second)
			lm, err := provider.CreateLanguageModel(pushCtx, provider.Type(providerName), apiKey, baseURL, model)
			pushCancel()
			if err != nil {
				m.logger.Warn("failed to create language model for config push",
					"agent", agentName, "model", model, "error", err)
			} else {
				inst.loop.UpdateModel(lm, model, providerName)
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
