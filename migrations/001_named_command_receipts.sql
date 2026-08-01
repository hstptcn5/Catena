PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS catena_command_receipts (
    command_id TEXT PRIMARY KEY,
    command_name TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    actor_id TEXT,
    permission TEXT,
    database_status TEXT NOT NULL CHECK (database_status IN ('committed', 'rejected')),
    result_json TEXT,
    error_code TEXT,
    error_message TEXT,
    created_at_ms INTEGER NOT NULL,
    committed_at_ms INTEGER,
    completed_at_ms INTEGER,
    UNIQUE (command_name, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_catena_receipts_created
    ON catena_command_receipts(created_at_ms, command_id);

CREATE TABLE IF NOT EXISTS catena_command_events (
    command_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    topic TEXT NOT NULL,
    destination TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (command_id, event_id),
    FOREIGN KEY (command_id)
        REFERENCES catena_command_receipts(command_id)
        ON DELETE CASCADE,
    FOREIGN KEY (event_id)
        REFERENCES anetac_outbox(id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_catena_command_events_event
    ON catena_command_events(event_id);
