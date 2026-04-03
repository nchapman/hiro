package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LogEntry represents a single application log record.
type LogEntry struct {
	ID         int64          `json:"id"`
	Level      string         `json:"level"`
	Message    string         `json:"message"`
	Component  string         `json:"component,omitempty"`
	InstanceID string         `json:"instance_id,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
	CreatedAt  time.Time      `json:"time"`
}

// LogQuery specifies filters for querying logs.
type LogQuery struct {
	Level     string // "" = all; exact match (e.g. "WARN")
	Component string // "" = all
	Search    string // substring match on message
	Before    int64  // cursor: return rows with id < Before (0 = from latest)
	Limit     int    // default 200, max 1000
}

// InsertLogs batch-inserts log entries in a single transaction.
func (d *DB) InsertLogs(ctx context.Context, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning log insert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO logs (level, message, component, instance_id, attrs, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("preparing log insert: %w", err)
	}
	defer stmt.Close()

	for _, e := range entries {
		var attrsJSON *string
		if len(e.Attrs) > 0 {
			b, err := json.Marshal(e.Attrs)
			if err != nil {
				return fmt.Errorf("marshaling log attrs: %w", err)
			}
			s := string(b)
			attrsJSON = &s
		}

		var component, instanceID *string
		if e.Component != "" {
			component = &e.Component
		}
		if e.InstanceID != "" {
			instanceID = &e.InstanceID
		}

		createdAt := e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")

		_, err = stmt.ExecContext(ctx, e.Level, e.Message, component, instanceID, attrsJSON, createdAt)
		if err != nil {
			return fmt.Errorf("inserting log entry: %w", err)
		}
	}
	return tx.Commit()
}

// QueryLogs returns log entries matching the given filters, ordered newest-first.
// Supports cursor-based pagination via the Before field.
func (d *DB) QueryLogs(ctx context.Context, opts LogQuery) ([]LogEntry, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	var conditions []string
	var args []any

	if opts.Before > 0 {
		conditions = append(conditions, "id < ?")
		args = append(args, opts.Before)
	}
	if opts.Level != "" {
		conditions = append(conditions, "level = ?")
		args = append(args, opts.Level)
	}
	if opts.Component != "" {
		conditions = append(conditions, "component = ?")
		args = append(args, opts.Component)
	}
	if opts.Search != "" {
		escaped := strings.NewReplacer(`%`, `\%`, `_`, `\_`).Replace(opts.Search)
		conditions = append(conditions, `message LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escaped+"%")
	}

	query := "SELECT id, level, message, component, instance_id, attrs, created_at FROM logs"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying logs: %w", err)
	}
	defer rows.Close()

	var results []LogEntry
	for rows.Next() {
		var e LogEntry
		var component, instanceID, attrsJSON sql.NullString
		var createdAt string

		if err := rows.Scan(&e.ID, &e.Level, &e.Message, &component, &instanceID, &attrsJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning log row: %w", err)
		}

		if component.Valid {
			e.Component = component.String
		}
		if instanceID.Valid {
			e.InstanceID = instanceID.String
		}
		if attrsJSON.Valid {
			_ = json.Unmarshal([]byte(attrsJSON.String), &e.Attrs)
		}
		e.CreatedAt = parseTimeLayout("2006-01-02T15:04:05.000Z", createdAt)

		results = append(results, e)
	}
	return results, rows.Err()
}

// PruneLogs deletes log entries older than maxAge and returns the count deleted.
func (d *DB) PruneLogs(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge).Format("2006-01-02T15:04:05.000Z")
	result, err := d.db.ExecContext(ctx, "DELETE FROM logs WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning logs: %w", err)
	}
	return result.RowsAffected()
}

// LogSources returns the distinct component values from the logs table.
func (d *DB) LogSources(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT DISTINCT component FROM logs WHERE component IS NOT NULL ORDER BY component",
	)
	if err != nil {
		return nil, fmt.Errorf("querying log sources: %w", err)
	}
	defer rows.Close()

	var sources []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scanning log source: %w", err)
		}
		sources = append(sources, s)
	}
	return sources, rows.Err()
}
