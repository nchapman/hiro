package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/nchapman/hiro/internal/config"
	"github.com/nchapman/hiro/internal/ipc"
	platformdb "github.com/nchapman/hiro/internal/platform/db"
)

// RestoreInstances reads persistent instances from the platform database
// and restarts them. Call once after NewManager.
func (m *Manager) RestoreInstances(ctx context.Context) error {
	if m.pdb == nil {
		return nil
	}

	instances, err := m.pdb.ListInstances("", "")
	if err != nil {
		return fmt.Errorf("listing instances from db: %w", err)
	}

	// Separate stopped instances from running ones. Stopped instances are
	// registered in two passes: first without groups (so all parents are
	// in the registry), then groups are derived with parent intersection.
	type restoreEntry struct {
		dbInst platformdb.Instance
		cfg    config.AgentConfig
		mode   config.AgentMode
	}
	var stopped, toStart []restoreEntry

	var cleaned int
	for _, dbInst := range instances {
		mode := config.AgentMode(dbInst.Mode)
		if !mode.IsPersistent() {
			m.pdb.DeleteInstance(dbInst.ID)
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
		if dbInst.Status == "stopped" {
			stopped = append(stopped, entry)
		} else {
			toStart = append(toStart, entry)
		}
	}

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
				NodeID:      ipc.NodeID(e.dbInst.NodeID),
			},
			agentName: e.cfg.Name,
			nodeID:    ipc.NodeID(e.dbInst.NodeID),
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
	var restored int
	for _, e := range stopped {
		if m.uidPool != nil {
			groups := m.resolveSupplementaryGroups(e.cfg, e.dbInst.ParentID)
			m.mu.Lock()
			m.instances[e.dbInst.ID].groups = groups
			m.mu.Unlock()
		}
		m.logger.Info("restored stopped instance",
			"id", e.dbInst.ID, "name", e.cfg.Name)
		restored++
	}

	// Start running instances.
	for _, e := range toStart {
		// Verify instance dir exists.
		if _, err := os.Stat(m.instanceDir(e.dbInst.ID)); os.IsNotExist(err) {
			m.logger.Warn("instance dir missing, removing orphaned DB record",
				"id", e.dbInst.ID, "agent", e.dbInst.AgentName)
			m.pdb.DeleteInstance(e.dbInst.ID)
			continue
		}

		// Resume the latest session if one exists, otherwise create a new one.
		var sessionID string
		sess, ok, sessErr := m.pdb.LatestSessionByInstance(e.dbInst.ID)
		if sessErr != nil {
			m.logger.Warn("failed to query latest session, creating new one",
				"instance", e.dbInst.ID, "error", sessErr)
		}
		if ok {
			sessionID = sess.ID
			m.logger.Info("resuming existing session",
				"instance", e.dbInst.ID, "session", sessionID, "agent", e.dbInst.AgentName)
		} else {
			sessionID = uuid.Must(uuid.NewV7()).String()
			m.logger.Info("creating new session (no previous session found)",
				"instance", e.dbInst.ID, "session", sessionID, "agent", e.dbInst.AgentName)
		}
		// Pass empty display name/desc — startInstance reads persona.md for existing instances.
		// Use the persisted node_id so instances restart on their original node.
		_, err = m.startInstance(ctx, e.dbInst.ID, sessionID, e.cfg, e.dbInst.ParentID, e.mode, ipc.NodeID(e.dbInst.NodeID), "", "")
		if err != nil {
			m.logger.Warn("failed to restore instance",
				"id", e.dbInst.ID, "agent", e.dbInst.AgentName, "error", err)
			continue
		}

		// Restore per-instance config (model override, reasoning effort).
		if instCfg, cfgErr := m.pdb.GetInstanceConfig(e.dbInst.ID); cfgErr == nil {
			if instCfg.ModelOverride != "" || instCfg.ReasoningEffort != "" {
				effort := instCfg.ReasoningEffort
				if err := m.UpdateInstanceConfig(ctx, e.dbInst.ID, instCfg.ModelOverride, &effort); err != nil {
					m.logger.Warn("failed to restore instance config",
						"instance", e.dbInst.ID, "error", err)
				}
			}
		}
		restored++
	}

	if restored > 0 || cleaned > 0 {
		m.logger.Info("instance restore complete", "restored", restored, "ephemeral_cleaned", cleaned)
	}
	return nil
}
