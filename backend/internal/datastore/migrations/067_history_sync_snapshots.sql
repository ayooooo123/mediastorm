-- +goose Up
-- Remote history keys a bidirectional history sync last saw on each provider.
-- A key present here but missing from a later full remote listing means the
-- play was removed on the provider, so the full export must not re-add it.
CREATE TABLE IF NOT EXISTS history_sync_snapshots (
    snapshot_key TEXT PRIMARY KEY,
    body BYTEA NOT NULL,
    key_count INTEGER NOT NULL DEFAULT 0,
    synced_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS history_sync_snapshots;
