package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Instance status constants.
const (
	statusRunning = "running"
	statusStopped = "stopped"
)

// Instance represents a row in the instances table.
//
// Storage rule: only non-derivable data is persisted. Derived state
// (effective tools, supplementary groups, resolved model/provider)
// is recomputed from agent definitions at startup. If it can be
// reconstructed from config files, it does not belong in this table.
type Instance struct {
	ID        string
	AgentName string
	Mode      string // "ephemeral", "persistent"
	ParentID  string // empty if root
	NodeID    string // cluster node ("home" for local)
	Status    string // "running", "stopped"
	CreatedAt time.Time
	StoppedAt *time.Time
}

// CreateInstance inserts a new instance.
func (d *DB) CreateInstance(ctx context.Context, inst Instance) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	var parentID *string
	if inst.ParentID != "" {
		parentID = &inst.ParentID
	}
	nodeID := inst.NodeID
	if nodeID == "" {
		nodeID = "home"
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO instances (id, agent_name, mode, parent_id, node_id, status) VALUES (?, ?, ?, ?, ?, ?)`,
		inst.ID, inst.AgentName, inst.Mode, parentID, nodeID, statusRunning,
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
func (d *DB) GetInstance(ctx context.Context, id string) (Instance, error) {
	var inst Instance
	var parentID sql.NullString
	var createdAt string
	var stoppedAt sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, agent_name, mode, parent_id, node_id, status, created_at, stopped_at
		 FROM instances WHERE id = ?`, id,
	).Scan(&inst.ID, &inst.AgentName, &inst.Mode, &parentID, &inst.NodeID, &inst.Status, &createdAt, &stoppedAt)
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
func (d *DB) ListInstances(ctx context.Context, parentID, status string) ([]Instance, error) {
	query := "SELECT id, agent_name, mode, parent_id, node_id, status, created_at, stopped_at FROM instances WHERE 1=1"
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

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanInstances(rows)
}

// ListChildInstances returns direct children of an instance.
func (d *DB) ListChildInstances(ctx context.Context, parentID string) ([]Instance, error) {
	return d.ListInstances(ctx, parentID, "")
}

// UpdateInstanceStatus sets the instance status. If status is "stopped",
// stopped_at is set to now.
func (d *DB) UpdateInstanceStatus(ctx context.Context, id, status string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	var stoppedAt *string
	if status == statusStopped {
		now := time.Now().UTC().Format(sqliteTimeFormat)
		stoppedAt = &now
	}
	result, err := d.db.ExecContext(ctx,
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
func (d *DB) DeleteInstance(ctx context.Context, id string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	result, err := d.db.ExecContext(ctx, "DELETE FROM instances WHERE id = ?", id)
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

// IsInstanceDescendant returns true if targetID is a descendant of ancestorID
// in the instance tree. Detects cycles and enforces a maximum traversal depth.
func (d *DB) IsInstanceDescendant(ctx context.Context, targetID, ancestorID string) (bool, error) {
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
		err := d.db.QueryRowContext(ctx, "SELECT parent_id FROM instances WHERE id = ?", current).Scan(&parentID)
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
		if err := rows.Scan(&inst.ID, &inst.AgentName, &inst.Mode, &parentID, &inst.NodeID, &inst.Status, &createdAt, &stoppedAt); err != nil {
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
