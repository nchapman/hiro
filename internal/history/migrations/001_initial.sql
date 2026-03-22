-- Raw messages (never deleted — this is the "lossless" part)
CREATE TABLE messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    seq        INTEGER NOT NULL UNIQUE,
    role       TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    raw_json   TEXT    NOT NULL,  -- JSON-serialized fantasy.Message for faithful reconstruction
    tokens     INTEGER NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Summary DAG
CREATE TABLE summaries (
    id            TEXT    PRIMARY KEY,
    kind          TEXT    NOT NULL CHECK (kind IN ('leaf', 'condensed')),
    depth         INTEGER NOT NULL,
    content       TEXT    NOT NULL,
    tokens        INTEGER NOT NULL,
    earliest_at   TEXT    NOT NULL,
    latest_at     TEXT    NOT NULL,
    source_tokens INTEGER NOT NULL,
    model         TEXT,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Links leaf summaries to their source messages
CREATE TABLE summary_messages (
    summary_id TEXT    NOT NULL REFERENCES summaries(id),
    message_id INTEGER NOT NULL REFERENCES messages(id),
    ordinal    INTEGER NOT NULL,
    PRIMARY KEY (summary_id, message_id)
);

-- Links condensed summaries to their child summaries
CREATE TABLE summary_parents (
    child_id  TEXT    NOT NULL REFERENCES summaries(id),
    parent_id TEXT    NOT NULL REFERENCES summaries(id),
    ordinal   INTEGER NOT NULL,
    PRIMARY KEY (child_id, parent_id)
);

-- Ordered view of what goes into context assembly.
-- Each item is either a raw message or a summary.
CREATE TABLE context_items (
    ordinal    INTEGER PRIMARY KEY,
    item_type  TEXT    NOT NULL CHECK (item_type IN ('message', 'summary')),
    message_id INTEGER REFERENCES messages(id),
    summary_id TEXT    REFERENCES summaries(id),
    CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);

-- Full-text search indexes.
-- content= makes these "external content" FTS tables backed by the real tables.
-- content_rowid= tells FTS5 which column to use as the rowid for joins.
CREATE VIRTUAL TABLE messages_fts USING fts5(content, content=messages, content_rowid=id);
CREATE VIRTUAL TABLE summaries_fts USING fts5(content, content=summaries, content_rowid=rowid);

-- Triggers to keep FTS indexes in sync.
-- The rowid in the FTS table must match the rowid of the source table.
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER summaries_ai AFTER INSERT ON summaries BEGIN
    INSERT INTO summaries_fts(rowid, content) VALUES (new.rowid, new.content);
END;
