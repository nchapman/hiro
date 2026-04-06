package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TriggerDef is the typed trigger definition stored as JSON.
type TriggerDef struct {
	Type string `json:"type"`           // "cron", "once"
	Expr string `json:"expr,omitempty"` // cron expression (type=cron)
	At   string `json:"at,omitempty"`   // absolute UTC time (type=once)
}

// Subscription represents a row in the subscriptions table.
type Subscription struct {
	ID         string
	InstanceID string
	Name       string
	Trigger    TriggerDef
	Message    string
	Status     string // "active", "paused"
	NextFire   *time.Time
	LastFired  *time.Time
	FireCount  int
	ErrorCount int
	LastError  string
	CreatedAt  time.Time
}

// CreateSubscription inserts a new subscription.
func (d *DB) CreateSubscription(ctx context.Context, sub Subscription) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	triggerJSON, err := json.Marshal(sub.Trigger)
	if err != nil {
		return fmt.Errorf("marshaling trigger: %w", err)
	}
	var nextFire *string
	if sub.NextFire != nil {
		s := sub.NextFire.UTC().Format(sqliteTimeFormat)
		nextFire = &s
	}
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO subscriptions (id, instance_id, name, trigger, message, status, next_fire)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.InstanceID, sub.Name, string(triggerJSON), sub.Message, sub.Status, nextFire,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("subscription %q on instance %s: %w", sub.Name, sub.InstanceID, ErrDuplicate)
		}
		return fmt.Errorf("inserting subscription: %w", err)
	}
	return nil
}

// GetSubscription retrieves a subscription by ID.
func (d *DB) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	var sub Subscription
	var triggerJSON string
	var nextFire, lastFired sql.NullString
	var lastError sql.NullString
	var createdAt string

	err := d.db.QueryRowContext(ctx,
		`SELECT id, instance_id, name, trigger, message, status, next_fire, last_fired,
		        fire_count, error_count, last_error, created_at
		 FROM subscriptions WHERE id = ?`, id,
	).Scan(&sub.ID, &sub.InstanceID, &sub.Name, &triggerJSON, &sub.Message, &sub.Status,
		&nextFire, &lastFired, &sub.FireCount, &sub.ErrorCount, &lastError, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, fmt.Errorf("subscription %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Subscription{}, err
	}
	if err := json.Unmarshal([]byte(triggerJSON), &sub.Trigger); err != nil {
		return Subscription{}, fmt.Errorf("parsing trigger: %w", err)
	}
	if nextFire.Valid {
		t := parseTime(nextFire.String)
		sub.NextFire = &t
	}
	if lastFired.Valid {
		t := parseTime(lastFired.String)
		sub.LastFired = &t
	}
	if lastError.Valid {
		sub.LastError = lastError.String
	}
	sub.CreatedAt = parseTime(createdAt)
	return sub, nil
}

// GetSubscriptionByName retrieves a subscription by instance ID and name.
func (d *DB) GetSubscriptionByName(ctx context.Context, instanceID, name string) (Subscription, error) {
	var id string
	err := d.db.QueryRowContext(ctx,
		`SELECT id FROM subscriptions WHERE instance_id = ? AND name = ?`, instanceID, name,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, fmt.Errorf("subscription %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Subscription{}, err
	}
	return d.GetSubscription(ctx, id)
}

// ListSubscriptionsByInstance returns all subscriptions for an instance.
func (d *DB) ListSubscriptionsByInstance(ctx context.Context, instanceID string) ([]Subscription, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, instance_id, name, trigger, message, status, next_fire, last_fired,
		        fire_count, error_count, last_error, created_at
		 FROM subscriptions WHERE instance_id = ? ORDER BY created_at`, instanceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

// ListActiveSubscriptions returns all active subscriptions with a non-null
// next_fire, ordered by next_fire ascending (soonest first).
func (d *DB) ListActiveSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, instance_id, name, trigger, message, status, next_fire, last_fired,
		        fire_count, error_count, last_error, created_at
		 FROM subscriptions WHERE status = 'active' AND next_fire IS NOT NULL
		 ORDER BY next_fire ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

// UpdateSubscriptionFired records a successful fire event.
func (d *DB) UpdateSubscriptionFired(ctx context.Context, id string, firedAt time.Time, nextFire *time.Time) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	fired := firedAt.UTC().Format(sqliteTimeFormat)
	var next *string
	if nextFire != nil {
		s := nextFire.UTC().Format(sqliteTimeFormat)
		next = &s
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE subscriptions SET last_fired = ?, fire_count = fire_count + 1,
		        next_fire = ?, error_count = 0, last_error = NULL
		 WHERE id = ?`,
		fired, next, id,
	)
	if err != nil {
		return fmt.Errorf("updating subscription after fire: %w", err)
	}
	return checkSubRowsAffected(result, id)
}

// UpdateSubscriptionError records a fire error.
func (d *DB) UpdateSubscriptionError(ctx context.Context, id string, nextFire *time.Time, errMsg string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	var next *string
	if nextFire != nil {
		s := nextFire.UTC().Format(sqliteTimeFormat)
		next = &s
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE subscriptions SET error_count = error_count + 1, last_error = ?, next_fire = ?
		 WHERE id = ?`,
		errMsg, next, id,
	)
	if err != nil {
		return fmt.Errorf("updating subscription error: %w", err)
	}
	return checkSubRowsAffected(result, id)
}

// UpdateSubscriptionStatus sets the subscription status and optionally
// updates next_fire (pass nil to leave unchanged, pass a zero-time pointer
// to clear it).
func (d *DB) UpdateSubscriptionStatus(ctx context.Context, id, status string, nextFire *time.Time) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	if nextFire != nil {
		var next *string
		if !nextFire.IsZero() {
			s := nextFire.UTC().Format(sqliteTimeFormat)
			next = &s
		}
		result, err := d.db.ExecContext(ctx,
			`UPDATE subscriptions SET status = ?, next_fire = ? WHERE id = ?`,
			status, next, id,
		)
		if err != nil {
			return fmt.Errorf("updating subscription status: %w", err)
		}
		return checkSubRowsAffected(result, id)
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE subscriptions SET status = ? WHERE id = ?`,
		status, id,
	)
	if err != nil {
		return fmt.Errorf("updating subscription status: %w", err)
	}
	return checkSubRowsAffected(result, id)
}

// PauseInstanceSubscriptions pauses all active subscriptions for an instance.
func (d *DB) PauseInstanceSubscriptions(ctx context.Context, instanceID string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'paused' WHERE instance_id = ? AND status = 'active'`,
		instanceID,
	)
	if err != nil {
		return fmt.Errorf("pausing subscriptions for instance %s: %w", instanceID, err)
	}
	return nil
}

// ResumeInstanceSubscriptions reactivates all paused subscriptions for an instance
// and returns the updated rows. The caller must recompute next_fire afterward.
func (d *DB) ResumeInstanceSubscriptions(ctx context.Context, instanceID string) ([]Subscription, error) {
	d.writeMu.Lock()
	_, err := d.db.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'active' WHERE instance_id = ? AND status = 'paused'`,
		instanceID,
	)
	d.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("resuming subscriptions for instance %s: %w", instanceID, err)
	}

	rows, err := d.db.QueryContext(ctx,
		`SELECT id, instance_id, name, trigger, message, status, next_fire, last_fired,
		        fire_count, error_count, last_error, created_at
		 FROM subscriptions WHERE instance_id = ? AND status = 'active'
		 ORDER BY created_at`, instanceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

// ListAllSubscriptions returns all subscriptions across all instances,
// ordered by instance_id then created_at.
func (d *DB) ListAllSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, instance_id, name, trigger, message, status, next_fire, last_fired,
		        fire_count, error_count, last_error, created_at
		 FROM subscriptions ORDER BY instance_id, created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

// DeleteSubscription removes a subscription.
func (d *DB) DeleteSubscription(ctx context.Context, id string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	result, err := d.db.ExecContext(ctx, "DELETE FROM subscriptions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting subscription: %w", err)
	}
	return checkSubRowsAffected(result, id)
}

// DeleteSubscriptionByName removes a subscription by instance ID and name.
func (d *DB) DeleteSubscriptionByName(ctx context.Context, instanceID, name string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	result, err := d.db.ExecContext(ctx, "DELETE FROM subscriptions WHERE instance_id = ? AND name = ?", instanceID, name)
	if err != nil {
		return fmt.Errorf("deleting subscription: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("subscription %q: %w", name, ErrNotFound)
	}
	return nil
}

func checkSubRowsAffected(result sql.Result, id string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("subscription %s: %w", id, ErrNotFound)
	}
	return nil
}

func scanSubscriptions(rows *sql.Rows) ([]Subscription, error) {
	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		var triggerJSON string
		var nextFire, lastFired, lastError sql.NullString
		var createdAt string
		if err := rows.Scan(&sub.ID, &sub.InstanceID, &sub.Name, &triggerJSON, &sub.Message, &sub.Status,
			&nextFire, &lastFired, &sub.FireCount, &sub.ErrorCount, &lastError, &createdAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(triggerJSON), &sub.Trigger); err != nil {
			return nil, fmt.Errorf("parsing trigger: %w", err)
		}
		if nextFire.Valid {
			t := parseTime(nextFire.String)
			sub.NextFire = &t
		}
		if lastFired.Valid {
			t := parseTime(lastFired.String)
			sub.LastFired = &t
		}
		if lastError.Valid {
			sub.LastError = lastError.String
		}
		sub.CreatedAt = parseTime(createdAt)
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}
