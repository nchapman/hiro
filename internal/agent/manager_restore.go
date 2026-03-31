package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/nchapman/hivebot/internal/config"
	"github.com/nchapman/hivebot/internal/ipc"
)

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

	var restored, cleaned int
	for _, dbInst := range instances {
		mode := config.AgentMode(dbInst.Mode)
		if !mode.IsPersistent() {
			// Clean up stale ephemeral instances from db and disk.
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

		if dbInst.Status == "stopped" {
			inst := &instance{
				info: InstanceInfo{
					ID:          dbInst.ID,
					Name:        cfg.Name,
					Mode:        mode,
					Description: cfg.Description,
					ParentID:    dbInst.ParentID,
					Status:      InstanceStatusStopped,
					Model:       m.resolveModel(),
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
			restored++
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
		sess, ok, sessErr := m.pdb.LatestSessionByInstance(dbInst.ID)
		if sessErr != nil {
			m.logger.Warn("failed to query latest session, creating new one",
				"instance", dbInst.ID, "error", sessErr)
		}
		if ok {
			sessionID = sess.ID
			m.logger.Info("resuming existing session",
				"instance", dbInst.ID, "session", sessionID, "agent", dbInst.AgentName)
		} else {
			sessionID = uuid.Must(uuid.NewV7()).String()
			m.logger.Info("creating new session (no previous session found)",
				"instance", dbInst.ID, "session", sessionID, "agent", dbInst.AgentName)
		}
		_, err = m.startInstance(ctx, dbInst.ID, sessionID, cfg, dbInst.ParentID, mode, ipc.HomeNodeID)
		if err != nil {
			m.logger.Warn("failed to restore instance",
				"id", dbInst.ID, "agent", dbInst.AgentName, "error", err)
			continue
		}

		// Restore per-instance config (model override, reasoning effort).
		if instCfg, cfgErr := m.pdb.GetInstanceConfig(dbInst.ID); cfgErr == nil {
			if instCfg.ModelOverride != "" || instCfg.ReasoningEffort != "" {
				effort := instCfg.ReasoningEffort
				if err := m.UpdateInstanceConfig(ctx, dbInst.ID, instCfg.ModelOverride, &effort); err != nil {
					m.logger.Warn("failed to restore instance config",
						"instance", dbInst.ID, "error", err)
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
