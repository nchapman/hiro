package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UsageEvent represents a single LLM call's token consumption.
// Multiple events may share a Turn number when a single chat turn
// produces multiple inference steps (e.g., tool-use loops).
type UsageEvent struct {
	ID               int64
	SessionID        string
	Model            string
	Provider         string
	Turn             int64
	InputTokens      int64
	OutputTokens     int64
	ReasoningTokens  int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Cost             float64
	CreatedAt        time.Time
}

// UsageSummary aggregates usage across multiple events.
type UsageSummary struct {
	TotalInputTokens      int64
	TotalOutputTokens     int64
	TotalReasoningTokens  int64
	TotalCacheReadTokens  int64
	TotalCacheWriteTokens int64
	TotalCost             float64
	EventCount            int64
}

// ModelUsage aggregates usage per model.
type ModelUsage struct {
	Model    string
	Provider string
	UsageSummary
}

// DailyUsage aggregates usage per day.
type DailyUsage struct {
	Date string // "2006-01-02"
	UsageSummary
}

// RecordTurnUsage inserts multiple usage events (one per inference step)
// as a single turn within a transaction. The turn number is auto-assigned.
// Turn numbering starts at 1; turn 0 is reserved for legacy pre-migration rows.
func (d *DB) RecordTurnUsage(ctx context.Context, events []UsageEvent) error {
	if len(events) == 0 {
		return nil
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning turn usage tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Determine next turn number atomically within the transaction.
	var maxTurn sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT MAX(turn) FROM usage_events WHERE session_id = ?`, events[0].SessionID,
	).Scan(&maxTurn)
	if err != nil {
		return fmt.Errorf("querying max turn: %w", err)
	}
	turn := int64(1)
	if maxTurn.Valid && maxTurn.Int64 >= 1 {
		turn = maxTurn.Int64 + 1
	}

	for _, e := range events {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO usage_events (session_id, model, provider, turn, input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_write_tokens, cost)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.SessionID, e.Model, e.Provider, turn,
			e.InputTokens, e.OutputTokens, e.ReasoningTokens,
			e.CacheReadTokens, e.CacheWriteTokens, e.Cost,
		)
		if err != nil {
			return fmt.Errorf("inserting usage event: %w", err)
		}
	}
	return tx.Commit()
}

// GetLastUsageEvent returns the most recent usage event for a session.
// Returns a zero UsageEvent and false if no tracked events exist.
// Turn 0 (legacy pre-migration rows) is excluded.
func (d *DB) GetLastUsageEvent(ctx context.Context, sessionID string) (UsageEvent, bool, error) {
	var e UsageEvent
	var createdAt string
	err := d.db.QueryRowContext(ctx,
		`SELECT id, session_id, model, provider, turn,
		        input_tokens, output_tokens, reasoning_tokens,
		        cache_read_tokens, cache_write_tokens, cost, created_at
		 FROM usage_events WHERE session_id = ? AND turn > 0 ORDER BY id DESC LIMIT 1`, sessionID,
	).Scan(&e.ID, &e.SessionID, &e.Model, &e.Provider, &e.Turn,
		&e.InputTokens, &e.OutputTokens, &e.ReasoningTokens,
		&e.CacheReadTokens, &e.CacheWriteTokens, &e.Cost, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UsageEvent{}, false, nil
		}
		return UsageEvent{}, false, err
	}
	e.CreatedAt = parseTime(createdAt)
	return e, true, nil
}

// GetLastTurnUsage returns aggregated usage for the most recent turn in a session.
// A turn may contain multiple steps (LLM calls). Returns false if no tracked
// turns exist. Turn 0 (legacy pre-migration rows) is excluded.
func (d *DB) GetLastTurnUsage(ctx context.Context, sessionID string) (UsageSummary, bool, error) {
	var u UsageSummary
	err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(cache_read_tokens),0),
		        COALESCE(SUM(cache_write_tokens),0), COALESCE(SUM(cost),0), COUNT(*)
		 FROM usage_events
		 WHERE session_id = ? AND turn > 0
		   AND turn = (SELECT MAX(turn) FROM usage_events WHERE session_id = ? AND turn > 0)`,
		sessionID, sessionID,
	).Scan(&u.TotalInputTokens, &u.TotalOutputTokens,
		&u.TotalReasoningTokens, &u.TotalCacheReadTokens,
		&u.TotalCacheWriteTokens, &u.TotalCost, &u.EventCount)
	if err != nil {
		return UsageSummary{}, false, err
	}
	if u.EventCount == 0 {
		return UsageSummary{}, false, nil
	}
	return u, true, nil
}

// GetSessionUsage returns aggregated usage for a session.
func (d *DB) GetSessionUsage(ctx context.Context, sessionID string) (UsageSummary, error) {
	return d.queryUsageSummary(ctx,
		`SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(cache_read_tokens),0),
		        COALESCE(SUM(cache_write_tokens),0), COALESCE(SUM(cost),0), COUNT(*)
		 FROM usage_events WHERE session_id = ?`, sessionID,
	)
}

// GetTotalUsage returns aggregated usage across all sessions.
func (d *DB) GetTotalUsage(ctx context.Context) (UsageSummary, error) {
	return d.queryUsageSummary(ctx,
		`SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(cache_read_tokens),0),
		        COALESCE(SUM(cache_write_tokens),0), COALESCE(SUM(cost),0), COUNT(*)
		 FROM usage_events`,
	)
}

// GetUsageByModel returns usage aggregated per model.
func (d *DB) GetUsageByModel(ctx context.Context) ([]ModelUsage, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT model, provider,
		        COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(cache_read_tokens),0),
		        COALESCE(SUM(cache_write_tokens),0), COALESCE(SUM(cost),0), COUNT(*)
		 FROM usage_events GROUP BY model, provider ORDER BY SUM(cost) DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(
			&m.Model, &m.Provider,
			&m.TotalInputTokens, &m.TotalOutputTokens,
			&m.TotalReasoningTokens, &m.TotalCacheReadTokens,
			&m.TotalCacheWriteTokens, &m.TotalCost, &m.EventCount,
		); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// GetUsageByDay returns usage aggregated per day.
func (d *DB) GetUsageByDay(ctx context.Context, limit int) ([]DailyUsage, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT date(created_at) as day,
		        COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(cache_read_tokens),0),
		        COALESCE(SUM(cache_write_tokens),0), COALESCE(SUM(cost),0), COUNT(*)
		 FROM usage_events GROUP BY day ORDER BY day DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DailyUsage
	for rows.Next() {
		var du DailyUsage
		if err := rows.Scan(
			&du.Date,
			&du.TotalInputTokens, &du.TotalOutputTokens,
			&du.TotalReasoningTokens, &du.TotalCacheReadTokens,
			&du.TotalCacheWriteTokens, &du.TotalCost, &du.EventCount,
		); err != nil {
			return nil, err
		}
		results = append(results, du)
	}
	return results, rows.Err()
}

func (d *DB) queryUsageSummary(ctx context.Context, query string, args ...any) (UsageSummary, error) {
	var u UsageSummary
	err := d.db.QueryRowContext(ctx, query, args...).Scan(
		&u.TotalInputTokens, &u.TotalOutputTokens,
		&u.TotalReasoningTokens, &u.TotalCacheReadTokens,
		&u.TotalCacheWriteTokens, &u.TotalCost, &u.EventCount,
	)
	return u, err
}
