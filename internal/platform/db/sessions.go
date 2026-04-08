package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// parseTime parses a SQLite datetime string, logging a warning on failure.
func parseTime(s string) time.Time {
	return parseTimeLayout(sqliteTimeFormat, s)
}

// parseTimeLayout parses a time string with the given layout, logging a warning on failure.
func parseTimeLayout(layout, s string) time.Time {
	t, err := time.Parse(layout, s)
	if err != nil {
		slog.Warn("failed to parse timestamp from database", "value", s, "error", err)
	}
	return t
}

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// ErrDuplicate is returned when an insert violates a uniqueness constraint.
var ErrDuplicate = errors.New("already exists")

// isUniqueViolation checks if an error is a SQLite UNIQUE constraint violation.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Session represents a row in the sessions table.
type Session struct {
	ID             string
	InstanceID     string // parent instance
	AgentName      string
	Mode           string // "ephemeral", "persistent"
	ParentID       string // empty if root
	SubscriptionID string // non-empty for triggered sessions
	ChannelType    string // "web", "telegram", "slack", "agent", "trigger"
	ChannelID      string // qualifier within type (e.g. chat ID, parent instance ID)
	Status         string // "running", "stopped"
	CreatedAt      time.Time
	StoppedAt      *time.Time
}

// CreateSession inserts a new session.
func (d *DB) CreateSession(ctx context.Context, s Session) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	var parentID *string
	if s.ParentID != "" {
		parentID = &s.ParentID
	}
	var instanceID *string
	if s.InstanceID != "" {
		instanceID = &s.InstanceID
	}
	var subscriptionID *string
	if s.SubscriptionID != "" {
		subscriptionID = &s.SubscriptionID
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO sessions (id, agent_name, mode, parent_id, status, instance_id, subscription_id, channel_type, channel_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.AgentName, s.Mode, parentID, "running", instanceID, subscriptionID, s.ChannelType, s.ChannelID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("session %s: %w", s.ID, ErrDuplicate)
		}
		return fmt.Errorf("inserting session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by ID.
func (d *DB) GetSession(ctx context.Context, id string) (Session, error) {
	var s Session
	var parentID sql.NullString
	var createdAt string
	var stoppedAt sql.NullString
	var instanceID sql.NullString
	var subscriptionID sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, instance_id, agent_name, mode, parent_id, subscription_id, channel_type, channel_id, status, created_at, stopped_at
		 FROM sessions WHERE id = ?`, id,
	).Scan(&s.ID, &instanceID, &s.AgentName, &s.Mode, &parentID, &subscriptionID, &s.ChannelType, &s.ChannelID, &s.Status, &createdAt, &stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Session{}, err
	}
	if instanceID.Valid {
		s.InstanceID = instanceID.String
	}
	if parentID.Valid {
		s.ParentID = parentID.String
	}
	if subscriptionID.Valid {
		s.SubscriptionID = subscriptionID.String
	}
	s.CreatedAt = parseTime(createdAt)
	if stoppedAt.Valid {
		t := parseTime(stoppedAt.String)
		s.StoppedAt = &t
	}
	return s, nil
}

// ListSessions returns all sessions matching the given filters.
// Pass empty strings to skip a filter.
func (d *DB) ListSessions(ctx context.Context, parentID, status string) ([]Session, error) {
	query := "SELECT id, instance_id, agent_name, mode, parent_id, subscription_id, channel_type, channel_id, status, created_at, stopped_at FROM sessions WHERE 1=1"
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

	return scanSessions(rows)
}

// ListChildSessions returns direct children of a session.
func (d *DB) ListChildSessions(ctx context.Context, parentID string) ([]Session, error) {
	return d.ListSessions(ctx, parentID, "")
}

// ListSessionsByInstance returns all sessions belonging to an instance.
func (d *DB) ListSessionsByInstance(ctx context.Context, instanceID string) ([]Session, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, instance_id, agent_name, mode, parent_id, subscription_id, channel_type, channel_id, status, created_at, stopped_at
		 FROM sessions WHERE instance_id = ? ORDER BY created_at`, instanceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessions(rows)
}

// LatestSessionByInstance returns the most recently created session for an instance.
// Returns the session and true if found, zero value and false if not found.
// Returns an error for database failures (distinct from "not found").
func (d *DB) LatestSessionByInstance(ctx context.Context, instanceID string) (Session, bool, error) {
	var s Session
	var parentID sql.NullString
	var subscriptionID sql.NullString
	var createdAt string
	var stoppedAt sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, instance_id, agent_name, mode, parent_id, subscription_id, channel_type, channel_id, status, created_at, stopped_at
		 FROM sessions WHERE instance_id = ? ORDER BY created_at DESC LIMIT 1`, instanceID,
	).Scan(&s.ID, &s.InstanceID, &s.AgentName, &s.Mode, &parentID, &subscriptionID, &s.ChannelType, &s.ChannelID, &s.Status, &createdAt, &stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("querying latest session for instance %s: %w", instanceID, err)
	}
	if parentID.Valid {
		s.ParentID = parentID.String
	}
	if subscriptionID.Valid {
		s.SubscriptionID = subscriptionID.String
	}
	s.CreatedAt = parseTime(createdAt)
	if stoppedAt.Valid {
		t := parseTime(stoppedAt.String)
		s.StoppedAt = &t
	}
	return s, true, nil
}

// LatestSessionByChannel returns the most recently created session for an
// instance + channel pair. Used by EnsureSession to resume existing sessions.
func (d *DB) LatestSessionByChannel(ctx context.Context, instanceID, channelType, channelID string) (Session, bool, error) {
	var s Session
	var parentID sql.NullString
	var subscriptionID sql.NullString
	var createdAt string
	var stoppedAt sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, instance_id, agent_name, mode, parent_id, subscription_id, channel_type, channel_id, status, created_at, stopped_at
		 FROM sessions WHERE instance_id = ? AND channel_type = ? AND channel_id = ? ORDER BY created_at DESC LIMIT 1`,
		instanceID, channelType, channelID,
	).Scan(&s.ID, &s.InstanceID, &s.AgentName, &s.Mode, &parentID, &subscriptionID, &s.ChannelType, &s.ChannelID, &s.Status, &createdAt, &stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("querying latest session for instance %s channel %s/%s: %w", instanceID, channelType, channelID, err)
	}
	if parentID.Valid {
		s.ParentID = parentID.String
	}
	if subscriptionID.Valid {
		s.SubscriptionID = subscriptionID.String
	}
	s.CreatedAt = parseTime(createdAt)
	if stoppedAt.Valid {
		t := parseTime(stoppedAt.String)
		s.StoppedAt = &t
	}
	return s, true, nil
}

// ListSessionsByChannelType returns all sessions for an instance filtered by channel type.
// Useful for the sessions viewer UI.
func (d *DB) ListSessionsByChannelType(ctx context.Context, instanceID, channelType string) ([]Session, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, instance_id, agent_name, mode, parent_id, subscription_id, channel_type, channel_id, status, created_at, stopped_at
		 FROM sessions WHERE instance_id = ? AND channel_type = ? ORDER BY created_at`, instanceID, channelType,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessions(rows)
}

// UpdateSessionStatus sets the session status. If status is "stopped",
// stopped_at is set to now.
func (d *DB) UpdateSessionStatus(ctx context.Context, id, status string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	var stoppedAt *string
	if status == statusStopped {
		now := time.Now().UTC().Format(sqliteTimeFormat)
		stoppedAt = &now
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE sessions SET status = ?, stopped_at = ? WHERE id = ?`,
		status, stoppedAt, id,
	)
	if err != nil {
		return fmt.Errorf("updating session status: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("session %s not found", id)
	}
	return nil
}

// LinkSessionToSubscription sets the subscription_id on an existing session.
// This links a triggered session to its subscription for later lookup.
func (d *DB) LinkSessionToSubscription(ctx context.Context, sessionID, subscriptionID string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx,
		`UPDATE sessions SET subscription_id = ? WHERE id = ?`,
		subscriptionID, sessionID,
	)
	if err != nil {
		return fmt.Errorf("linking session %s to subscription %s: %w", sessionID, subscriptionID, err)
	}
	return nil
}

// DeleteSession removes a session and all its data (cascades).
func (d *DB) DeleteSession(ctx context.Context, id string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	result, err := d.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("session %s not found", id)
	}
	return nil
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	var sessions []Session
	for rows.Next() {
		var s Session
		var instanceID, parentID, subscriptionID sql.NullString
		var createdAt string
		var stoppedAt sql.NullString
		if err := rows.Scan(&s.ID, &instanceID, &s.AgentName, &s.Mode, &parentID, &subscriptionID, &s.ChannelType, &s.ChannelID, &s.Status, &createdAt, &stoppedAt); err != nil {
			return nil, err
		}
		if instanceID.Valid {
			s.InstanceID = instanceID.String
		}
		if parentID.Valid {
			s.ParentID = parentID.String
		}
		if subscriptionID.Valid {
			s.SubscriptionID = subscriptionID.String
		}
		s.CreatedAt = parseTime(createdAt)
		if stoppedAt.Valid {
			t := parseTime(stoppedAt.String)
			s.StoppedAt = &t
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// SessionBySubscription returns the session associated with a subscription,
// if one exists. Returns the session and true if found.
func (d *DB) SessionBySubscription(ctx context.Context, subscriptionID string) (Session, bool, error) {
	var s Session
	var instanceID, parentID, subID sql.NullString
	var createdAt string
	var stoppedAt sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, instance_id, agent_name, mode, parent_id, subscription_id, channel_type, channel_id, status, created_at, stopped_at
		 FROM sessions WHERE subscription_id = ? LIMIT 1`, subscriptionID,
	).Scan(&s.ID, &instanceID, &s.AgentName, &s.Mode, &parentID, &subID, &s.ChannelType, &s.ChannelID, &s.Status, &createdAt, &stoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("querying session for subscription %s: %w", subscriptionID, err)
	}
	if instanceID.Valid {
		s.InstanceID = instanceID.String
	}
	if parentID.Valid {
		s.ParentID = parentID.String
	}
	if subID.Valid {
		s.SubscriptionID = subID.String
	}
	s.CreatedAt = parseTime(createdAt)
	if stoppedAt.Valid {
		t := parseTime(stoppedAt.String)
		s.StoppedAt = &t
	}
	return s, true, nil
}

// SessionMessageCounts returns the number of non-meta messages for each of
// the given session IDs. Sessions with zero messages are omitted from the map.
func (d *DB) SessionMessageCounts(ctx context.Context, sessionIDs []string) (map[string]int, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(sessionIDs))
	args := make([]any, len(sessionIDs))
	for i, id := range sessionIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf( //nolint:gosec // G201: placeholders are "?" literals, not user input — values are parameterized via args
		"SELECT session_id, COUNT(*) FROM messages WHERE session_id IN (%s) AND meta = 0 GROUP BY session_id",
		strings.Join(placeholders, ","),
	)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("counting session messages: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int, len(sessionIDs))
	for rows.Next() {
		var sid string
		var count int
		if err := rows.Scan(&sid, &count); err != nil {
			return nil, err
		}
		counts[sid] = count
	}
	return counts, rows.Err()
}
