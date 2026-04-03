package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Instance represents a row in the instances table.
type Instance struct {
	ID        string
	AgentName string
	Mode      string // "ephemeral", "persistent"
	ParentID  string // empty if root
	Status    string // "running", "stopped"
	CreatedAt time.Time
	StoppedAt *time.Time
}

// CreateInstance inserts a new instance.
func (d *DB) CreateInstance(inst Instance) error {
	var parentID *string
	if inst.ParentID != "" {
		parentID = &inst.ParentID
	}
	_, err := d.db.Exec(
		`INSERT INTO instances (id, agent_name, mode, parent_id, status) VALUES (?, ?, ?, ?, ?)`,
		inst.ID, inst.AgentName, inst.Mode, parentID, "running",
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("instance %s: %w", inst.ID, ErrDuplicate)
		}
		return fmt.Errorf("inserting instance: %w", err)
	}
	return nil
}

// GetInstance retrieves an instance by ID.
func (d *DB) GetInstance(id string) (Instance, error) {
	var inst Instance
	var parentID sql.NullString
	var createdAt string
	var stoppedAt sql.NullString
	err := d.db.QueryRow(
		`SELECT id, agent_name, mode, parent_id, status, created_at, stopped_at
		 FROM instances WHERE id = ?`, id,
	).Scan(&inst.ID, &inst.AgentName, &inst.Mode, &parentID, &inst.Status, &createdAt, &stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, fmt.Errorf("instance %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Instance{}, err
	}
	if parentID.Valid {
		inst.ParentID = parentID.String
	}
	inst.CreatedAt = parseTime(createdAt)
	if stoppedAt.Valid {
		t := parseTime(stoppedAt.String)
		inst.StoppedAt = &t
	}
	return inst, nil
}

// ListInstances returns all instances matching the given filters.
// Pass empty strings to skip a filter.
func (d *DB) ListInstances(parentID, status string) ([]Instance, error) {
	query := "SELECT id, agent_name, mode, parent_id, status, created_at, stopped_at FROM instances WHERE 1=1"
	var args []any

	if parentID != "" {
		query += " AND parent_id = ?"
		args = append(args, parentID)
	}
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanInstances(rows)
}

// ListChildInstances returns direct children of an instance.
func (d *DB) ListChildInstances(parentID string) ([]Instance, error) {
	return d.ListInstances(parentID, "")
}

// UpdateInstanceStatus sets the instance status. If status is "stopped",
// stopped_at is set to now.
func (d *DB) UpdateInstanceStatus(id, status string) error {
	var stoppedAt *string
	if status == "stopped" {
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		stoppedAt = &now
	}
	result, err := d.db.Exec(
		`UPDATE instances SET status = ?, stopped_at = ? WHERE id = ?`,
		status, stoppedAt, id,
	)
	if err != nil {
		return fmt.Errorf("updating instance status: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("instance %s not found", id)
	}
	return nil
}

// DeleteInstance removes an instance and all its data (cascades to sessions).
func (d *DB) DeleteInstance(id string) error {
	result, err := d.db.Exec("DELETE FROM instances WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting instance: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("instance %s not found", id)
	}
	return nil
}

// InstanceConfig holds per-instance configuration as a JSON blob.
type InstanceConfig struct {
	ModelOverride   string `json:"model_override,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// GetInstanceConfig reads the per-instance config JSON.
func (d *DB) GetInstanceConfig(instanceID string) (InstanceConfig, error) {
	var raw string
	err := d.db.QueryRow("SELECT COALESCE(config, '{}') FROM instances WHERE id = ?", instanceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return InstanceConfig{}, fmt.Errorf("instance %s: %w", instanceID, ErrNotFound)
	}
	if err != nil {
		return InstanceConfig{}, err
	}
	var cfg InstanceConfig
	if raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return InstanceConfig{}, fmt.Errorf("parsing instance config: %w", err)
		}
	}
	return cfg, nil
}

// UpdateInstanceConfig writes the per-instance config JSON.
func (d *DB) UpdateInstanceConfig(instanceID string, cfg InstanceConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling instance config: %w", err)
	}
	result, err := d.db.Exec("UPDATE instances SET config = ? WHERE id = ?", string(raw), instanceID)
	if err != nil {
		return fmt.Errorf("updating instance config: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("instance %s not found", instanceID)
	}
	return nil
}

// IsInstanceDescendant returns true if targetID is a descendant of ancestorID
// in the instance tree. Detects cycles and enforces a maximum traversal depth.
func (d *DB) IsInstanceDescendant(targetID, ancestorID string) (bool, error) {
	const maxDepth = 100
	visited := make(map[string]bool, maxDepth)
	current := targetID
	for range maxDepth {
		if visited[current] {
			return false, fmt.Errorf("cycle detected in instance tree at %s", current)
		}
		visited[current] = true
		if current == ancestorID {
			return true, nil
		}
		var parentID sql.NullString
		err := d.db.QueryRow("SELECT parent_id FROM instances WHERE id = ?", current).Scan(&parentID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // node not in DB, therefore not a descendant
		}
		if err != nil {
			return false, fmt.Errorf("looking up parent of instance %s: %w", current, err)
		}
		if !parentID.Valid {
			return false, nil
		}
		current = parentID.String
	}
	return false, fmt.Errorf("instance tree exceeds maximum depth (%d)", maxDepth)
}

func scanInstances(rows *sql.Rows) ([]Instance, error) {
	var instances []Instance
	for rows.Next() {
		var inst Instance
		var parentID sql.NullString
		var createdAt string
		var stoppedAt sql.NullString
		if err := rows.Scan(&inst.ID, &inst.AgentName, &inst.Mode, &parentID, &inst.Status, &createdAt, &stoppedAt); err != nil {
			return nil, err
		}
		if parentID.Valid {
			inst.ParentID = parentID.String
		}
		inst.CreatedAt = parseTime(createdAt)
		if stoppedAt.Valid {
			t := parseTime(stoppedAt.String)
			inst.StoppedAt = &t
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}
