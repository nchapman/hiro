package history

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Message represents a stored conversation message.
type Message struct {
	ID        int64
	Seq       int
	Role      string
	Content   string
	RawJSON   string // JSON-serialized fantasy.Message
	Tokens    int
	CreatedAt time.Time
}

// Summary represents a node in the summary DAG.
type Summary struct {
	ID           string
	Kind         string // "leaf" or "condensed"
	Depth        int
	Content      string
	Tokens       int
	EarliestAt   time.Time
	LatestAt     time.Time
	SourceTokens int
	Model        string
	CreatedAt    time.Time
}

// ContextItem represents an entry in the ordered context view.
type ContextItem struct {
	Ordinal   int
	ItemType  string // "message" or "summary"
	MessageID *int64
	SummaryID *string
}

// SearchResult represents a match from full-text search.
type SearchResult struct {
	ID      string // message ID (as string) or summary ID
	Type    string // "message" or "summary"
	Snippet string
	Rank    float64
}

// Store provides persistence for conversation history via SQLite.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) a history database at the given path
// and runs any pending migrations.
func OpenStore(path string) (*Store, error) {
	// Ensure the DB file is created with restricted permissions (0600).
	// The session directory should already be 0700.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("creating database file: %w", err)
	}
	f.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// SQLite is single-writer; serialize all access through one connection.
	db.SetMaxOpenConns(1)

	// Enable WAL mode and foreign keys
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("setting %s: %w", pragma, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	return s, nil
}

// OpenStoreReadOnly opens an existing history database in read-only mode.
// This is used by the control plane to read an agent's history without
// interfering with the agent process's WAL checkpointing. The caller must
// close the store after use.
func OpenStoreReadOnly(path string) (*Store, error) {
	dsn := path + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting busy_timeout: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate runs embedded SQL migration files in order,
// skipping any that have already been applied.
func (s *Store) migrate() error {
	// Create the migrations tracking table if it doesn't exist
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		filename TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	// Read all migration files
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}

	// Sort by filename (lexicographic = chronological with NNN_ prefix)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Check if already applied
		var count int
		err := s.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE filename = ?", entry.Name()).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking migration %s: %w", entry.Name(), err)
		}
		if count > 0 {
			continue
		}

		// Read and execute migration
		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", entry.Name(), err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(string(data)); err != nil {
			tx.Rollback()
			return fmt.Errorf("executing migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations (filename) VALUES (?)", entry.Name()); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// --- Messages ---

// AppendMessage stores a new message and adds it to the context items.
// Returns the new message's ID.
func (s *Store) AppendMessage(role, content, rawJSON string, tokens int) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Get next sequence number
	var maxSeq sql.NullInt64
	if err := tx.QueryRow("SELECT MAX(seq) FROM messages").Scan(&maxSeq); err != nil {
		return 0, err
	}
	seq := 1
	if maxSeq.Valid {
		seq = int(maxSeq.Int64) + 1
	}

	// Insert message
	result, err := tx.Exec(
		"INSERT INTO messages (seq, role, content, raw_json, tokens) VALUES (?, ?, ?, ?, ?)",
		seq, role, content, rawJSON, tokens,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting message: %w", err)
	}
	msgID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	// Get next context ordinal
	var maxOrd sql.NullInt64
	if err := tx.QueryRow("SELECT MAX(ordinal) FROM context_items").Scan(&maxOrd); err != nil {
		return 0, err
	}
	ord := 0
	if maxOrd.Valid {
		ord = int(maxOrd.Int64) + 1
	}

	// Add to context items
	if _, err := tx.Exec(
		"INSERT INTO context_items (ordinal, item_type, message_id) VALUES (?, 'message', ?)",
		ord, msgID,
	); err != nil {
		return 0, fmt.Errorf("inserting context item: %w", err)
	}

	return msgID, tx.Commit()
}

// GetMessage retrieves a message by ID.
func (s *Store) GetMessage(id int64) (Message, error) {
	var m Message
	var createdAt string
	err := s.db.QueryRow(
		"SELECT id, seq, role, content, raw_json, tokens, created_at FROM messages WHERE id = ?", id,
	).Scan(&m.ID, &m.Seq, &m.Role, &m.Content, &m.RawJSON, &m.Tokens, &createdAt)
	if err != nil {
		return Message{}, err
	}
	m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return m, nil
}

// GetMessages retrieves messages by their IDs, in order.
func (s *Store) GetMessages(ids []int64) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		"SELECT id, seq, role, content, raw_json, tokens, created_at FROM messages WHERE id IN (%s) ORDER BY seq",
		strings.Join(placeholders, ","),
	)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.Seq, &m.Role, &m.Content, &m.RawJSON, &m.Tokens, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// --- Summaries ---

// CreateSummary inserts a new summary record.
func (s *Store) CreateSummary(sum Summary) error {
	_, err := s.db.Exec(
		`INSERT INTO summaries (id, kind, depth, content, tokens, earliest_at, latest_at, source_tokens, model)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sum.ID, sum.Kind, sum.Depth, sum.Content, sum.Tokens,
		sum.EarliestAt.Format(time.RFC3339), sum.LatestAt.Format(time.RFC3339),
		sum.SourceTokens, sum.Model,
	)
	return err
}

// LinkSummaryMessages links a leaf summary to its source messages.
func (s *Store) LinkSummaryMessages(summaryID string, messageIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, msgID := range messageIDs {
		if _, err := tx.Exec(
			"INSERT INTO summary_messages (summary_id, message_id, ordinal) VALUES (?, ?, ?)",
			summaryID, msgID, i,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LinkSummaryParents links a condensed summary to its child summaries.
func (s *Store) LinkSummaryParents(parentID string, childIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, childID := range childIDs {
		if _, err := tx.Exec(
			"INSERT INTO summary_parents (child_id, parent_id, ordinal) VALUES (?, ?, ?)",
			childID, parentID, i,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSummary retrieves a summary by ID.
func (s *Store) GetSummary(id string) (Summary, error) {
	var sum Summary
	var earliest, latest, created string
	var model sql.NullString
	err := s.db.QueryRow(
		`SELECT id, kind, depth, content, tokens, earliest_at, latest_at, source_tokens, model, created_at
		 FROM summaries WHERE id = ?`, id,
	).Scan(&sum.ID, &sum.Kind, &sum.Depth, &sum.Content, &sum.Tokens,
		&earliest, &latest, &sum.SourceTokens, &model, &created)
	if err != nil {
		return Summary{}, err
	}
	sum.EarliestAt, _ = time.Parse(time.RFC3339, earliest)
	sum.LatestAt, _ = time.Parse(time.RFC3339, latest)
	sum.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	if model.Valid {
		sum.Model = model.String
	}
	return sum, nil
}

// GetSummarySourceMessages returns the source message IDs for a leaf summary.
func (s *Store) GetSummarySourceMessages(summaryID string) ([]int64, error) {
	rows, err := s.db.Query(
		"SELECT message_id FROM summary_messages WHERE summary_id = ? ORDER BY ordinal", summaryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetSummaryChildren returns the child summary IDs for a condensed summary.
func (s *Store) GetSummaryChildren(parentID string) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT child_id FROM summary_parents WHERE parent_id = ? ORDER BY ordinal", parentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- Context Items ---

// GetContextItems returns all context items in ordinal order.
func (s *Store) GetContextItems() ([]ContextItem, error) {
	rows, err := s.db.Query(
		"SELECT ordinal, item_type, message_id, summary_id FROM context_items ORDER BY ordinal",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ContextItem
	for rows.Next() {
		var ci ContextItem
		if err := rows.Scan(&ci.Ordinal, &ci.ItemType, &ci.MessageID, &ci.SummaryID); err != nil {
			return nil, err
		}
		items = append(items, ci)
	}
	return items, rows.Err()
}

// ReplaceContextItems replaces a range of context items (inclusive) with a
// single summary item. Used after compaction to swap messages/summaries
// for their new summary.
func (s *Store) ReplaceContextItems(startOrd, endOrd int, summaryID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete the old range
	if _, err := tx.Exec(
		"DELETE FROM context_items WHERE ordinal >= ? AND ordinal <= ?",
		startOrd, endOrd,
	); err != nil {
		return err
	}

	// Insert the summary at the start position
	if _, err := tx.Exec(
		"INSERT INTO context_items (ordinal, item_type, summary_id) VALUES (?, 'summary', ?)",
		startOrd, summaryID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// ContextTokenCount returns the total estimated tokens across all context items.
func (s *Store) ContextTokenCount() (int, error) {
	var total int
	err := s.db.QueryRow(`
		SELECT COALESCE(
			(SELECT SUM(m.tokens) FROM context_items ci
			 JOIN messages m ON ci.message_id = m.id
			 WHERE ci.item_type = 'message'), 0
		) + COALESCE(
			(SELECT SUM(s.tokens) FROM context_items ci
			 JOIN summaries s ON ci.summary_id = s.id
			 WHERE ci.item_type = 'summary'), 0
		)
	`).Scan(&total)
	return total, err
}

// --- Search ---

// sanitizeFTSQuery wraps a query as a phrase to prevent FTS5 operator injection.
func sanitizeFTSQuery(q string) string {
	q = strings.ReplaceAll(q, `"`, `""`)
	return `"` + q + `"`
}

// SearchMessages performs a full-text search over messages.
func (s *Store) SearchMessages(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if len(query) > 512 {
		return nil, fmt.Errorf("search query too long (max 512 bytes)")
	}
	query = sanitizeFTSQuery(query)
	rows, err := s.db.Query(`
		SELECT m.id, snippet(messages_fts, 0, '»', '«', '…', 32), rank
		FROM messages_fts
		JOIN messages m ON messages_fts.rowid = m.id
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("searching messages: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var id int64
		if err := rows.Scan(&id, &r.Snippet, &r.Rank); err != nil {
			return nil, err
		}
		r.ID = fmt.Sprintf("%d", id)
		r.Type = "message"
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchSummaries performs a full-text search over summaries.
func (s *Store) SearchSummaries(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if len(query) > 512 {
		return nil, fmt.Errorf("search query too long (max 512 bytes)")
	}
	query = sanitizeFTSQuery(query)
	rows, err := s.db.Query(`
		SELECT s.id, snippet(summaries_fts, 0, '»', '«', '…', 32), rank
		FROM summaries_fts
		JOIN summaries s ON summaries_fts.rowid = s.rowid
		WHERE summaries_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("searching summaries: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Snippet, &r.Rank); err != nil {
			return nil, err
		}
		r.Type = "summary"
		results = append(results, r)
	}
	return results, rows.Err()
}

// Search performs a full-text search over both messages and summaries.
func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	msgs, err := s.SearchMessages(query, limit)
	if err != nil {
		return nil, err
	}
	sums, err := s.SearchSummaries(query, limit)
	if err != nil {
		return nil, err
	}
	// Merge and sort by rank (lower is better in FTS5)
	all := append(msgs, sums...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].Rank < all[j].Rank
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// --- Stats ---

// MessageTokensOutsideTail returns the total tokens in messages that are
// in context_items but outside the most recent `tailSize` context items.
func (s *Store) MessageTokensOutsideTail(tailSize int) (int, error) {
	var total int
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(m.tokens), 0)
		FROM context_items ci
		JOIN messages m ON ci.message_id = m.id
		WHERE ci.item_type = 'message'
		  AND ci.ordinal NOT IN (
			SELECT ordinal FROM context_items ORDER BY ordinal DESC LIMIT ?
		  )
	`, tailSize).Scan(&total)
	return total, err
}

// OldestMessageContextItems returns the oldest N message-type context items
// outside the fresh tail, along with their message data. Used by compaction
// to select the next chunk to summarize.
func (s *Store) OldestMessageContextItems(tailSize, maxTokens int) ([]ContextItem, []Message, error) {
	// Get all context items except the tail
	rows, err := s.db.Query(`
		SELECT ci.ordinal, ci.item_type, ci.message_id, ci.summary_id,
		       m.id, m.seq, m.role, m.content, m.raw_json, m.tokens, m.created_at
		FROM context_items ci
		JOIN messages m ON ci.message_id = m.id
		WHERE ci.item_type = 'message'
		  AND ci.ordinal NOT IN (
			SELECT ordinal FROM context_items ORDER BY ordinal DESC LIMIT ?
		  )
		ORDER BY ci.ordinal ASC
	`, tailSize)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var items []ContextItem
	var msgs []Message
	tokenSum := 0

	for rows.Next() {
		var ci ContextItem
		var m Message
		var createdAt string
		if err := rows.Scan(
			&ci.Ordinal, &ci.ItemType, &ci.MessageID, &ci.SummaryID,
			&m.ID, &m.Seq, &m.Role, &m.Content, &m.RawJSON, &m.Tokens, &createdAt,
		); err != nil {
			return nil, nil, err
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)

		// Stop if adding this message would exceed token budget
		if tokenSum > 0 && tokenSum+m.Tokens > maxTokens {
			break
		}
		tokenSum += m.Tokens
		items = append(items, ci)
		msgs = append(msgs, m)
	}
	return items, msgs, rows.Err()
}

// ContiguousSummariesAtDepth returns the oldest contiguous run of summaries
// at the given depth from context_items. Used by condensation to find
// summaries eligible for merging.
func (s *Store) ContiguousSummariesAtDepth(depth, minCount int) ([]ContextItem, []Summary, error) {
	rows, err := s.db.Query(`
		SELECT ci.ordinal, ci.item_type, ci.message_id, ci.summary_id,
		       s.id, s.kind, s.depth, s.content, s.tokens,
		       s.earliest_at, s.latest_at, s.source_tokens, s.model, s.created_at
		FROM context_items ci
		JOIN summaries s ON ci.summary_id = s.id
		WHERE ci.item_type = 'summary' AND s.depth = ?
		ORDER BY ci.ordinal ASC
	`, depth)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var allItems []ContextItem
	var allSums []Summary

	for rows.Next() {
		var ci ContextItem
		var sum Summary
		var earliest, latest, created string
		var model sql.NullString
		if err := rows.Scan(
			&ci.Ordinal, &ci.ItemType, &ci.MessageID, &ci.SummaryID,
			&sum.ID, &sum.Kind, &sum.Depth, &sum.Content, &sum.Tokens,
			&earliest, &latest, &sum.SourceTokens, &model, &created,
		); err != nil {
			return nil, nil, err
		}
		sum.EarliestAt, _ = time.Parse(time.RFC3339, earliest)
		sum.LatestAt, _ = time.Parse(time.RFC3339, latest)
		sum.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		if model.Valid {
			sum.Model = model.String
		}
		allItems = append(allItems, ci)
		allSums = append(allSums, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Find longest contiguous run by ordinal
	if len(allItems) < minCount {
		return nil, nil, nil
	}

	bestStart, bestLen := 0, 1
	curStart, curLen := 0, 1
	for i := 1; i < len(allItems); i++ {
		if allItems[i].Ordinal == allItems[i-1].Ordinal+1 {
			curLen++
		} else {
			curStart = i
			curLen = 1
		}
		if curLen > bestLen {
			bestStart = curStart
			bestLen = curLen
		}
	}

	if bestLen < minCount {
		return nil, nil, nil
	}

	return allItems[bestStart : bestStart+bestLen], allSums[bestStart : bestStart+bestLen], nil
}

// MaxSummaryDepth returns the maximum depth of any summary in context_items,
// or -1 if there are no summaries.
func (s *Store) MaxSummaryDepth() (int, error) {
	var depth sql.NullInt64
	err := s.db.QueryRow(`
		SELECT MAX(s.depth) FROM context_items ci
		JOIN summaries s ON ci.summary_id = s.id
		WHERE ci.item_type = 'summary'
	`).Scan(&depth)
	if err != nil {
		return -1, err
	}
	if !depth.Valid {
		return -1, nil
	}
	return int(depth.Int64), nil
}

// RecentMessages returns the most recent N messages ordered oldest-first.
func (s *Store) RecentMessages(limit int) ([]Message, error) {
	rows, err := s.db.Query(`
		SELECT id, seq, role, content, raw_json, tokens, created_at
		FROM messages ORDER BY seq DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.Seq, &m.Role, &m.Content, &m.RawJSON, &m.Tokens, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to oldest-first order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}
