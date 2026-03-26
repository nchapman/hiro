-- Sessions: runtime state for each agent session.
CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    agent_name TEXT    NOT NULL,
    mode       TEXT    NOT NULL CHECK (mode IN ('ephemeral', 'persistent', 'coordinator')),
    parent_id  TEXT    REFERENCES sessions(id) ON DELETE CASCADE,
    status     TEXT    NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'stopped')),
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    stopped_at TEXT
);
CREATE INDEX idx_sessions_parent ON sessions(parent_id);
CREATE INDEX idx_sessions_agent  ON sessions(agent_name);
CREATE INDEX idx_sessions_status ON sessions(status);

-- Messages: conversation history across all sessions.
CREATE TABLE messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    role       TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    raw_json   TEXT    NOT NULL,
    tokens     INTEGER NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (session_id, seq)
);
CREATE INDEX idx_messages_session    ON messages(session_id);
CREATE INDEX idx_messages_created_at ON messages(created_at);

-- Summary DAG: compacted conversation summaries.
CREATE TABLE summaries (
    id            TEXT    PRIMARY KEY,
    session_id    TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
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
CREATE INDEX idx_summaries_session ON summaries(session_id);

-- Links leaf summaries to their source messages.
CREATE TABLE summary_messages (
    summary_id TEXT    NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ordinal    INTEGER NOT NULL,
    PRIMARY KEY (summary_id, message_id)
);

-- Links condensed summaries to their child summaries.
CREATE TABLE summary_parents (
    child_id  TEXT NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    parent_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    ordinal   INTEGER NOT NULL,
    PRIMARY KEY (child_id, parent_id)
);

-- Ordered context view per session: each item is a message or summary.
CREATE TABLE context_items (
    session_id TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    ordinal    INTEGER NOT NULL,
    item_type  TEXT    NOT NULL CHECK (item_type IN ('message', 'summary')),
    message_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
    summary_id TEXT    REFERENCES summaries(id) ON DELETE CASCADE,
    PRIMARY KEY (session_id, ordinal),
    CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);

-- Usage events: token consumption per LLM call.
CREATE TABLE usage_events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    model               TEXT    NOT NULL,
    provider            TEXT    NOT NULL,
    input_tokens        INTEGER NOT NULL DEFAULT 0,
    output_tokens       INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens  INTEGER NOT NULL DEFAULT 0,
    cost                REAL    NOT NULL DEFAULT 0.0,
    created_at          TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_usage_session    ON usage_events(session_id);
CREATE INDEX idx_usage_created_at ON usage_events(created_at);
CREATE INDEX idx_usage_model      ON usage_events(model);

-- Request log: optional full LLM request/response capture.
CREATE TABLE request_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    model       TEXT    NOT NULL,
    request     TEXT,
    response    TEXT,
    duration_ms INTEGER,
    error       TEXT,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_request_log_session ON request_log(session_id);

-- Full-text search indexes.
-- messages_fts uses external content (messages has INTEGER PK, safe for content_rowid).
CREATE VIRTUAL TABLE messages_fts USING fts5(content, content=messages, content_rowid=id);

-- summaries_fts is standalone (not external content) because summaries uses a
-- TEXT PRIMARY KEY, making implicit rowid unstable across deletes/re-inserts.
-- We store summary_id alongside content for join-back.
CREATE VIRTUAL TABLE summaries_fts USING fts5(summary_id, content);

-- Triggers to keep FTS indexes in sync.
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;
CREATE TRIGGER summaries_ai AFTER INSERT ON summaries BEGIN
    INSERT INTO summaries_fts(summary_id, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER summaries_ad AFTER DELETE ON summaries BEGIN
    DELETE FROM summaries_fts WHERE summary_id = old.id;
END;
