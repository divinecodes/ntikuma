CREATE TABLE IF NOT EXISTS chats (
                                     id          TEXT PRIMARY KEY,
                                     title       TEXT NOT NULL,
                                     created_at  INTEGER NOT NULL,
                                     updated_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS chats_updated ON chats(updated_at DESC);

CREATE TABLE IF NOT EXISTS messages (
                                        id          TEXT PRIMARY KEY,
                                        chat_id     TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    role        TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    content     TEXT NOT NULL,
    model       TEXT,
    fallback    INTEGER NOT NULL DEFAULT 0,
    attachments TEXT,
    created_at  INTEGER NOT NULL
    );

CREATE INDEX IF NOT EXISTS messages_chat ON messages(chat_id, created_at);

-- External-content FTS5 index over message text, kept in sync by triggers.
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    content='messages',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
                                               INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
