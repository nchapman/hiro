package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const maxSearchQueryLen = 512 // max byte length for FTS search queries

// Message represents a stored conversation message.
type Message struct {
	ID        int64
	SessionID string
	Seq       int
	Role      string
	Content   string
	RawJSON   string
	Tokens    int
	Meta      bool // Meta messages are visible to the model but hidden from the user's transcript.
	CreatedAt time.Time
}

// Summary represents a node in the summary DAG.
type Summary struct {
	ID           string
	SessionID    string
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
	SessionID string
	Ordinal   int
	ItemType  string // "message" or "summary"
	MessageID *int64
	SummaryID *string
}

// SearchResult represents a match from full-text search.
type SearchResult struct {
	ID        string
	SessionID string
	Type      string // "message" or "summary"
	Snippet   string
	Rank      float64
}

// --- Messages ---

// AppendMessage stores a new message and adds it to the context items.
// If meta is true, the message is visible to the model but hidden from the user's transcript.
func (d *DB) AppendMessage(ctx context.Context, sessionID, role, content, rawJSON string, tokens int, meta ...bool) (int64, error) {
	isMeta := len(meta) > 0 && meta[0]
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Get next sequence number for this session.
	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(seq) FROM messages WHERE session_id = ?", sessionID).Scan(&maxSeq); err != nil {
		return 0, err
	}
	seq := 1
	if maxSeq.Valid {
		seq = int(maxSeq.Int64) + 1
	}

	metaInt := 0
	if isMeta {
		metaInt = 1
	}
	result, err := tx.ExecContext(ctx,
		"INSERT INTO messages (session_id, seq, role, content, raw_json, tokens, meta) VALUES (?, ?, ?, ?, ?, ?, ?)",
		sessionID, seq, role, content, rawJSON, tokens, metaInt,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting message: %w", err)
	}
	msgID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	// Get next context ordinal for this session.
	var maxOrd sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(ordinal) FROM context_items WHERE session_id = ?", sessionID).Scan(&maxOrd); err != nil {
		return 0, err
	}
	ord := 0
	if maxOrd.Valid {
		ord = int(maxOrd.Int64) + 1
	}

	if _, err := tx.ExecContext(ctx,
		"INSERT INTO context_items (session_id, ordinal, item_type, message_id) VALUES (?, ?, 'message', ?)",
		sessionID, ord, msgID,
	); err != nil {
		return 0, fmt.Errorf("inserting context item: %w", err)
	}

	return msgID, tx.Commit()
}

// UpdateMessageTimestamp sets the created_at timestamp for a message.
// Used by tests that ingest historical data with known timestamps.
func (d *DB) UpdateMessageTimestamp(ctx context.Context, id int64, t time.Time) error {
	_, err := d.db.ExecContext(ctx, "UPDATE messages SET created_at = ? WHERE id = ?",
		t.Format("2006-01-02 15:04:05"), id)
	return err
}

// GetMessage retrieves a message by ID.
func (d *DB) GetMessage(ctx context.Context, id int64) (Message, error) {
	var m Message
	var createdAt string
	err := d.db.QueryRowContext(ctx,
		"SELECT id, session_id, seq, role, content, raw_json, tokens, meta, created_at FROM messages WHERE id = ?", id,
	).Scan(&m.ID, &m.SessionID, &m.Seq, &m.Role, &m.Content, &m.RawJSON, &m.Tokens, &m.Meta, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, fmt.Errorf("message %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Message{}, err
	}
	m.CreatedAt = parseTime(createdAt)
	return m, nil
}

// GetMessages retrieves messages by their IDs, in sequence order.
func (d *DB) GetMessages(ctx context.Context, ids []int64) ([]Message, error) {
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
		"SELECT id, session_id, seq, role, content, raw_json, tokens, meta, created_at FROM messages WHERE id IN (%s) ORDER BY seq",
		strings.Join(placeholders, ","),
	)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// RecentMessages returns the most recent N messages for a session, oldest first.
func (d *DB) RecentMessages(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, session_id, seq, role, content, raw_json, tokens, meta, created_at
		 FROM messages WHERE session_id = ? ORDER BY seq DESC LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	// Reverse to oldest-first.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// --- Summaries ---

// CreateSummary inserts a new summary record.
func (d *DB) CreateSummary(ctx context.Context, sum Summary) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO summaries (id, session_id, kind, depth, content, tokens, earliest_at, latest_at, source_tokens, model)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sum.ID, sum.SessionID, sum.Kind, sum.Depth, sum.Content, sum.Tokens,
		sum.EarliestAt.Format(time.RFC3339), sum.LatestAt.Format(time.RFC3339),
		sum.SourceTokens, sum.Model,
	)
	return err
}

// LinkSummaryMessages links a leaf summary to its source messages.
func (d *DB) LinkSummaryMessages(ctx context.Context, summaryID string, messageIDs []int64) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, msgID := range messageIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO summary_messages (summary_id, message_id, ordinal) VALUES (?, ?, ?)",
			summaryID, msgID, i,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LinkSummaryParents links a condensed summary to its child summaries.
func (d *DB) LinkSummaryParents(ctx context.Context, parentID string, childIDs []string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, childID := range childIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO summary_parents (child_id, parent_id, ordinal) VALUES (?, ?, ?)",
			childID, parentID, i,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSummary retrieves a summary by ID.
func (d *DB) GetSummary(ctx context.Context, id string) (Summary, error) {
	var sum Summary
	var earliest, latest, created string
	var model sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, session_id, kind, depth, content, tokens, earliest_at, latest_at, source_tokens, model, created_at
		 FROM summaries WHERE id = ?`, id,
	).Scan(&sum.ID, &sum.SessionID, &sum.Kind, &sum.Depth, &sum.Content, &sum.Tokens,
		&earliest, &latest, &sum.SourceTokens, &model, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, fmt.Errorf("summary %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Summary{}, err
	}
	sum.EarliestAt = parseTimeLayout(time.RFC3339, earliest)
	sum.LatestAt = parseTimeLayout(time.RFC3339, latest)
	sum.CreatedAt = parseTime(created)
	if model.Valid {
		sum.Model = model.String
	}
	return sum, nil
}

// GetSummarySourceMessages returns the source message IDs for a leaf summary.
func (d *DB) GetSummarySourceMessages(ctx context.Context, summaryID string) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx,
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
func (d *DB) GetSummaryChildren(ctx context.Context, parentID string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
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

// GetContextItems returns all context items for a session in ordinal order.
func (d *DB) GetContextItems(ctx context.Context, sessionID string) ([]ContextItem, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT session_id, ordinal, item_type, message_id, summary_id FROM context_items WHERE session_id = ? ORDER BY ordinal",
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ContextItem
	for rows.Next() {
		var ci ContextItem
		if err := rows.Scan(&ci.SessionID, &ci.Ordinal, &ci.ItemType, &ci.MessageID, &ci.SummaryID); err != nil {
			return nil, err
		}
		items = append(items, ci)
	}
	return items, rows.Err()
}

// ReplaceContextItems replaces a range of context items (inclusive) with a
// single summary item. Used after compaction.
func (d *DB) ReplaceContextItems(ctx context.Context, sessionID string, startOrd, endOrd int, summaryID string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM context_items WHERE session_id = ? AND ordinal >= ? AND ordinal <= ?",
		sessionID, startOrd, endOrd,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO context_items (session_id, ordinal, item_type, summary_id) VALUES (?, ?, 'summary', ?)",
		sessionID, startOrd, summaryID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// ContextTokenCount returns the total estimated tokens across all context items for a session.
func (d *DB) ContextTokenCount(ctx context.Context, sessionID string) (int, error) {
	var total int
	err := d.db.QueryRowContext(ctx, `
		SELECT COALESCE(
			(SELECT SUM(m.tokens) FROM context_items ci
			 JOIN messages m ON ci.message_id = m.id
			 WHERE ci.session_id = ? AND ci.item_type = 'message'), 0
		) + COALESCE(
			(SELECT SUM(s.tokens) FROM context_items ci
			 JOIN summaries s ON ci.summary_id = s.id
			 WHERE ci.session_id = ? AND ci.item_type = 'summary'), 0
		)
	`, sessionID, sessionID).Scan(&total)
	return total, err
}

// MessageTokensOutsideTail returns the total tokens in messages that are
// in context_items but outside the most recent tailSize items for a session.
func (d *DB) MessageTokensOutsideTail(ctx context.Context, sessionID string, tailSize int) (int, error) {
	var total int
	err := d.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(m.tokens), 0)
		FROM context_items ci
		JOIN messages m ON ci.message_id = m.id
		WHERE ci.session_id = ? AND ci.item_type = 'message'
		  AND ci.ordinal NOT IN (
			SELECT ordinal FROM context_items WHERE session_id = ? ORDER BY ordinal DESC LIMIT ?
		  )
	`, sessionID, sessionID, tailSize).Scan(&total)
	return total, err
}

// OldestMessageContextItems returns the oldest N message-type context items
// outside the fresh tail for a session, along with their message data.
func (d *DB) OldestMessageContextItems(ctx context.Context, sessionID string, tailSize, maxTokens int) ([]ContextItem, []Message, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT ci.session_id, ci.ordinal, ci.item_type, ci.message_id, ci.summary_id,
		       m.id, m.session_id, m.seq, m.role, m.content, m.raw_json, m.tokens, m.meta, m.created_at
		FROM context_items ci
		JOIN messages m ON ci.message_id = m.id
		WHERE ci.session_id = ? AND ci.item_type = 'message'
		  AND ci.ordinal NOT IN (
			SELECT ordinal FROM context_items WHERE session_id = ? ORDER BY ordinal DESC LIMIT ?
		  )
		ORDER BY ci.ordinal ASC
	`, sessionID, sessionID, tailSize)
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
			&ci.SessionID, &ci.Ordinal, &ci.ItemType, &ci.MessageID, &ci.SummaryID,
			&m.ID, &m.SessionID, &m.Seq, &m.Role, &m.Content, &m.RawJSON, &m.Tokens, &m.Meta, &createdAt,
		); err != nil {
			return nil, nil, err
		}
		m.CreatedAt = parseTime(createdAt)

		if tokenSum > 0 && tokenSum+m.Tokens > maxTokens {
			break
		}
		tokenSum += m.Tokens
		items = append(items, ci)
		msgs = append(msgs, m)
	}
	return items, msgs, rows.Err()
}

// ContiguousSummariesAtDepth returns the longest run of summaries at the given
// depth that are adjacent in the context ordering — no other context items
// (messages or summaries at other depths) exist between them.
//
// Note: adjacency is determined by context position, not by ordinal arithmetic.
// After ReplaceContextItems, ordinals have gaps (e.g., summaries at ordinals
// 5 and 21 with nothing between them are adjacent even though 5+1 != 21).
func (d *DB) ContiguousSummariesAtDepth(ctx context.Context, sessionID string, depth, minCount int) ([]ContextItem, []Summary, error) {
	// Fetch all context items to determine adjacency, then filter to
	// summaries at the target depth. Two summaries are adjacent if no
	// other context items appear between their positions in the full list.
	allCI, err := d.GetContextItems(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}

	// Build a list of indices into allCI that are summaries at the target depth.
	// We need to fetch the summary data for depth checking.
	type candidate struct {
		posInContext int // index in allCI
		ci           ContextItem
		sum          Summary
	}
	var candidates []candidate

	for idx, ci := range allCI {
		if ci.ItemType != "summary" || ci.SummaryID == nil {
			continue
		}
		sum, err := d.GetSummary(ctx, *ci.SummaryID)
		if err != nil {
			continue
		}
		if sum.Depth != depth {
			continue
		}
		candidates = append(candidates, candidate{posInContext: idx, ci: ci, sum: sum})
	}

	if len(candidates) < minCount {
		return nil, nil, nil
	}

	// Find longest run where candidates are adjacent in context position
	// (no other context items between them).
	bestStart, bestLen := 0, 1
	curStart, curLen := 0, 1
	for i := 1; i < len(candidates); i++ {
		if candidates[i].posInContext == candidates[i-1].posInContext+1 {
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

	run := candidates[bestStart : bestStart+bestLen]
	items := make([]ContextItem, len(run))
	sums := make([]Summary, len(run))
	for i, c := range run {
		items[i] = c.ci
		sums[i] = c.sum
	}
	return items, sums, nil
}

// MaxSummaryDepth returns the maximum depth of any summary in context_items
// for a session, or -1 if there are no summaries.
func (d *DB) MaxSummaryDepth(ctx context.Context, sessionID string) (int, error) {
	var depth sql.NullInt64
	err := d.db.QueryRowContext(ctx, `
		SELECT MAX(s.depth) FROM context_items ci
		JOIN summaries s ON ci.summary_id = s.id
		WHERE ci.session_id = ? AND ci.item_type = 'summary'
	`, sessionID).Scan(&depth)
	if err != nil {
		return -1, err
	}
	if !depth.Valid {
		return -1, nil
	}
	return int(depth.Int64), nil
}

// --- Search ---

func sanitizeFTSQuery(q string) string {
	q = strings.ReplaceAll(q, `"`, `""`)
	return `"` + q + `"`
}

// SearchMessages performs a full-text search over messages in a session.
func (d *DB) SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if len(query) > maxSearchQueryLen {
		return nil, fmt.Errorf("search query too long (max %d bytes)", maxSearchQueryLen)
	}
	query = sanitizeFTSQuery(query)
	rows, err := d.db.QueryContext(ctx, `
		SELECT m.id, m.session_id, snippet(messages_fts, 0, '»', '«', '…', 32), rank
		FROM messages_fts
		JOIN messages m ON messages_fts.rowid = m.id
		WHERE messages_fts MATCH ? AND m.session_id = ?
		ORDER BY rank
		LIMIT ?
	`, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("searching messages: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows, "message")
}

// SearchSummaries performs a full-text search over summaries in a session.
func (d *DB) SearchSummaries(ctx context.Context, sessionID, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if len(query) > maxSearchQueryLen {
		return nil, fmt.Errorf("search query too long (max %d bytes)", maxSearchQueryLen)
	}
	query = sanitizeFTSQuery(query)
	rows, err := d.db.QueryContext(ctx, `
		SELECT s.id, s.session_id, snippet(summaries_fts, 1, '»', '«', '…', 32), rank
		FROM summaries_fts
		JOIN summaries s ON summaries_fts.summary_id = s.id
		WHERE summaries_fts MATCH ? AND s.session_id = ?
		ORDER BY rank
		LIMIT ?
	`, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("searching summaries: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows, "summary")
}

// Search performs a full-text search over messages and summaries in a session.
func (d *DB) Search(ctx context.Context, sessionID, query string, limit int) ([]SearchResult, error) {
	msgs, err := d.SearchMessages(ctx, sessionID, query, limit)
	if err != nil {
		return nil, err
	}
	sums, err := d.SearchSummaries(ctx, sessionID, query, limit)
	if err != nil {
		return nil, err
	}
	all := make([]SearchResult, 0, len(msgs)+len(sums))
	all = append(all, msgs...)
	all = append(all, sums...)
	sort.Slice(all, func(i, j int) bool { return all[i].Rank < all[j].Rank })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var msgs []Message
	for rows.Next() {
		var m Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Seq, &m.Role, &m.Content, &m.RawJSON, &m.Tokens, &m.Meta, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(createdAt)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func scanSearchResults(rows *sql.Rows, resultType string) ([]SearchResult, error) {
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		r.Type = resultType
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Snippet, &r.Rank); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
