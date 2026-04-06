package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/nchapman/hiro/internal/config"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// restoreEntry holds the data needed to restore a single instance.
type restoreEntry struct {
	dbInst platformdb.Instance
	cfg    config.AgentConfig
	mode   config.AgentMode
}

// RestoreInstances reads persistent instances from the platform database
// and restarts them. Call once after NewManager.
func (m *Manager) RestoreInstances(ctx context.Context) error {
	if m.pdb == nil {
		return nil
	}

	instances, err := m.pdb.ListInstances(ctx, "", "")
	if err != nil {
		return fmt.Errorf("listing instances from db: %w", err)
	}

	// Separate stopped instances from running ones. Stopped instances are
	// registered in two passes: first without groups (so all parents are
	// in the registry), then groups are derived with parent intersection.
	var stopped, toStart []restoreEntry

	var cleaned int
	for _, dbInst := range instances {
		mode := config.AgentMode(dbInst.Mode)
		if !mode.IsPersistent() {
			if err := m.pdb.DeleteInstance(ctx, dbInst.ID); err != nil {
				m.logger.Warn("failed to delete ephemeral instance from DB", "id", dbInst.ID, "error", err)
			}
			os.RemoveAll(m.instanceDir(dbInst.ID))
			cleaned++
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

		entry := restoreEntry{dbInst, cfg, mode}
		if dbInst.Status == string(InstanceStatusStopped) {
			stopped = append(stopped, entry)
		} else {
			toStart = append(toStart, entry)
		}
	}

	// Register stopped instances in two passes: first without groups (so all
	// parents are in the registry), then resolve groups with parent intersection.
	restored := m.registerStoppedInstances(stopped)

	// Start running instances.
	for _, e := range toStart {
		if m.restoreRunningInstance(ctx, &e) {
			restored++
		}
	}

	if restored > 0 || cleaned > 0 {
		m.logger.Info("instance restore complete", "restored", restored, "ephemeral_cleaned", cleaned)
	}
	return nil
}

// registerStoppedInstances registers stopped instances in the manager's registry
// and resolves their supplementary groups. Returns the count registered.
func (m *Manager) registerStoppedInstances(stopped []restoreEntry) int {
	// Pass 1: register all stopped instances (without groups) so
	// parentGroupSet can find parents regardless of restore order.
	for _, e := range stopped {
		inst := &instance{
			info: InstanceInfo{
				ID:          e.dbInst.ID,
				Name:        e.cfg.Name,
				Mode:        e.mode,
				Description: e.cfg.Description,
				ParentID:    e.dbInst.ParentID,
				Status:      InstanceStatusStopped,
				Model:       m.resolveModelString(e.cfg.Model),
				NodeID:      e.dbInst.NodeID,
			},
			agentName: e.cfg.Name,
			nodeID:    e.dbInst.NodeID,
		}
		m.mu.Lock()
		m.instances[e.dbInst.ID] = inst
		if e.dbInst.ParentID != "" {
			m.children[e.dbInst.ParentID] = append(m.children[e.dbInst.ParentID], e.dbInst.ID)
		}
		m.mu.Unlock()
	}

	// Pass 2: resolve groups via the shared helper, now that all
	// stopped instances are registered and parentGroupSet works.
	for _, e := range stopped {
		if m.uidPool != nil {
			groups := m.resolveSupplementaryGroups(e.cfg, e.dbInst.ParentID)
			m.mu.Lock()
			m.instances[e.dbInst.ID].groups = groups
			m.mu.Unlock()
		}
		m.logger.Info("restored stopped instance",
			"id", e.dbInst.ID, "name", e.cfg.Name)
	}
	return len(stopped)
}

// restoreRunningInstance restarts a single persistent instance from its DB record.
// Returns true if the instance was successfully restored.
func (m *Manager) restoreRunningInstance(ctx context.Context, e *restoreEntry) bool {
	// Verify instance dir exists.
	if _, err := os.Stat(m.instanceDir(e.dbInst.ID)); os.IsNotExist(err) {
		m.logger.Warn("instance dir missing, removing orphaned DB record",
			"id", e.dbInst.ID, "agent", e.dbInst.AgentName)
		if err := m.pdb.DeleteInstance(ctx, e.dbInst.ID); err != nil {
			m.logger.Warn("failed to delete orphaned instance from DB", "id", e.dbInst.ID, "error", err)
		}
		return false
	}

	// Resume the latest session if one exists, otherwise create a new one.
	sessionID := m.resolveSessionForRestore(ctx, e.dbInst.ID, e.dbInst.AgentName)

	// Pass empty display name/desc — startInstance reads persona.md for existing instances.
	// Use the persisted node_id so instances restart on their original node.
	_, err := m.startInstance(ctx, e.dbInst.ID, sessionID, e.cfg, e.dbInst.ParentID, e.mode, e.dbInst.NodeID, "", "", "")
	if err != nil {
		m.logger.Warn("failed to restore instance",
			"id", e.dbInst.ID, "agent", e.dbInst.AgentName, "error", err)
		return false
	}

	// Restore per-instance config (model override, reasoning effort).
	m.restoreInstanceConfig(ctx, e.dbInst.ID)
	return true
}

// resolveSessionForRestore finds the latest session for an instance or creates a new ID.
func (m *Manager) resolveSessionForRestore(ctx context.Context, instanceID, agentName string) string {
	sess, ok, err := m.pdb.LatestSessionByInstance(ctx, instanceID)
	if err != nil {
		m.logger.Warn("failed to query latest session, creating new one",
			"instance", instanceID, "error", err)
	}
	if ok {
		m.logger.Info("resuming existing session",
			"instance", instanceID, "session", sess.ID, "agent", agentName)
		return sess.ID
	}
	sessionID := uuid.Must(uuid.NewV7()).String()
	m.logger.Info("creating new session (no previous session found)",
		"instance", instanceID, "session", sessionID, "agent", agentName)
	return sessionID
}

// restoreInstanceConfig restores per-instance config (model override, reasoning effort)
// from the platform database.
func (m *Manager) restoreInstanceConfig(ctx context.Context, instanceID string) {
	instCfg, err := m.pdb.GetInstanceConfig(ctx, instanceID)
	if err != nil {
		return
	}
	if instCfg.ModelOverride == "" && instCfg.ReasoningEffort == "" {
		return
	}
	effort := instCfg.ReasoningEffort
	if err := m.UpdateInstanceConfig(ctx, instanceID, instCfg.ModelOverride, &effort); err != nil {
		m.logger.Warn("failed to restore instance config",
			"instance", instanceID, "error", err)
	}
}
