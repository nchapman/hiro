package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// Session represents a row in the sessions table.
type Session struct {
	ID        string
	AgentName string
	Mode      string // "ephemeral", "persistent", "coordinator"
	ParentID  string // empty if root
	Status    string // "running", "stopped"
	CreatedAt time.Time
	StoppedAt *time.Time
}

// CreateSession inserts a new session.
func (d *DB) CreateSession(s Session) error {
	var parentID *string
	if s.ParentID != "" {
		parentID = &s.ParentID
	}
	_, err := d.db.Exec(
		`INSERT INTO sessions (id, agent_name, mode, parent_id, status) VALUES (?, ?, ?, ?, ?)`,
		s.ID, s.AgentName, s.Mode, parentID, "running",
	)
	if err != nil {
		return fmt.Errorf("inserting session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by ID.
func (d *DB) GetSession(id string) (Session, error) {
	var s Session
	var parentID sql.NullString
	var createdAt string
	var stoppedAt sql.NullString
	err := d.db.QueryRow(
		`SELECT id, agent_name, mode, parent_id, status, created_at, stopped_at
		 FROM sessions WHERE id = ?`, id,
	).Scan(&s.ID, &s.AgentName, &s.Mode, &parentID, &s.Status, &createdAt, &stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Session{}, err
	}
	if parentID.Valid {
		s.ParentID = parentID.String
	}
	s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	if stoppedAt.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", stoppedAt.String)
		s.StoppedAt = &t
	}
	return s, nil
}

// ListSessions returns all sessions matching the given filters.
// Pass empty strings to skip a filter.
func (d *DB) ListSessions(parentID, status string) ([]Session, error) {
	query := "SELECT id, agent_name, mode, parent_id, status, created_at, stopped_at FROM sessions WHERE 1=1"
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

	return scanSessions(rows)
}

// ListChildSessions returns direct children of a session.
func (d *DB) ListChildSessions(parentID string) ([]Session, error) {
	return d.ListSessions(parentID, "")
}

// UpdateSessionStatus sets the session status. If status is "stopped",
// stopped_at is set to now.
func (d *DB) UpdateSessionStatus(id, status string) error {
	var stoppedAt *string
	if status == "stopped" {
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		stoppedAt = &now
	}
	result, err := d.db.Exec(
		`UPDATE sessions SET status = ?, stopped_at = ? WHERE id = ?`,
		status, stoppedAt, id,
	)
	if err != nil {
		return fmt.Errorf("updating session status: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %s not found", id)
	}
	return nil
}

// DeleteSession removes a session and all its data (cascades).
func (d *DB) DeleteSession(id string) error {
	result, err := d.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %s not found", id)
	}
	return nil
}

// IsDescendant returns true if candidateID is a descendant of ancestorID.
func (d *DB) IsDescendant(ancestorID, candidateID string) (bool, error) {
	// Walk up from candidate to root, checking for ancestor.
	current := candidateID
	for {
		if current == ancestorID {
			return true, nil
		}
		var parentID sql.NullString
		err := d.db.QueryRow("SELECT parent_id FROM sessions WHERE id = ?", current).Scan(&parentID)
		if err != nil {
			return false, err
		}
		if !parentID.Valid {
			return false, nil
		}
		current = parentID.String
	}
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	var sessions []Session
	for rows.Next() {
		var s Session
		var parentID sql.NullString
		var createdAt string
		var stoppedAt sql.NullString
		if err := rows.Scan(&s.ID, &s.AgentName, &s.Mode, &parentID, &s.Status, &createdAt, &stoppedAt); err != nil {
			return nil, err
		}
		if parentID.Valid {
			s.ParentID = parentID.String
		}
		s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		if stoppedAt.Valid {
			t, _ := time.Parse("2006-01-02 15:04:05", stoppedAt.String)
			s.StoppedAt = &t
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}
