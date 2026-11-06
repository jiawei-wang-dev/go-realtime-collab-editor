package clientstorage

const schema = `
CREATE TABLE IF NOT EXISTS client_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS client_documents (
    doc_id TEXT PRIMARY KEY,
    server_version INTEGER NOT NULL CHECK(server_version >= 0),
    content TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pending_edits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    base_version INTEGER NOT NULL CHECK(base_version >= 0),
    operation_json TEXT NOT NULL,
    status TEXT NOT NULL,
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK(retry_count >= 0),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(doc_id, request_id)
);
CREATE INDEX IF NOT EXISTS pending_edits_order ON pending_edits(doc_id, id);
CREATE TABLE IF NOT EXISTS refresh_attempts (
    request_id TEXT PRIMARY KEY,
    credential_request_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
PRAGMA user_version = 2;
`
