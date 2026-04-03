// Package db provides the unified platform database (hiro.db).
//
// All session, message, history, usage, and request log data is stored in a
// single SQLite database. The control plane is the sole writer; agent workers
// never touch this database.
//
// Storage rule: only persist non-derivable data. Derived state (effective
// tools, supplementary groups, resolved model/provider) is recomputed from
// agent definitions and control plane config at startup. If it can be
// reconstructed from config files on disk, it does not belong here.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/nchapman/hiro/internal/platform/fsperm"
	_ "modernc.org/sqlite" // SQLite driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	sqlitePoolSize = 4 // concurrent readers in WAL mode
)

// DB wraps a SQLite database connection for the platform.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the platform database at the given path
// and runs any pending migrations.
func Open(path string) (*DB, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, fsperm.FilePrivate) //nolint:gosec // database path from startup
	if err != nil {
		return nil, fmt.Errorf("creating database file: %w", err)
	}
	f.Close()

	// Use _pragma DSN parameters so every pooled connection gets the same
	// pragmas automatically. This allows multiple concurrent readers in WAL
	// mode while a single writer holds busy_timeout for lock contention.
	dsn := (&url.URL{
		Path: path,
		RawQuery: url.Values{
			"_pragma": {
				"journal_mode(WAL)",   // persists in DB header; idempotent per connection
				"foreign_keys(1)",     // connection-scoped; must be set per connection
				"busy_timeout(10000)", // connection-scoped; must be set per connection
			},
		}.Encode(),
	}).String()

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Allow concurrent readers in WAL mode. Writes still serialize via
	// SQLite's internal lock, but reads proceed without blocking.
	conn.SetMaxOpenConns(sqlitePoolSize)
	conn.SetMaxIdleConns(sqlitePoolSize)

	d := &DB{db: conn}
	if err := d.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	return d, nil
}

// Close runs optimizer maintenance and closes the database connection.
// PRAGMA optimize runs ANALYZE on tables whose stats are stale, keeping
// the query planner effective. WAL checkpoint truncates the write-ahead
// log file to reclaim disk space.
func (d *DB) Close() error {
	ctx := context.Background()
	_, _ = d.db.ExecContext(ctx, "PRAGMA optimize")
	_, _ = d.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return d.db.Close()
}

// migrate runs embedded SQL migration files in order,
// skipping any that have already been applied.
func (d *DB) migrate() error {
	ctx := context.Background()
	_, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		filename   TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		var count int
		err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE filename = ?", entry.Name()).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking migration %s: %w", entry.Name(), err)
		}
		if count > 0 {
			continue
		}

		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", entry.Name(), err)
		}

		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("executing migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (filename) VALUES (?)", entry.Name()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}
