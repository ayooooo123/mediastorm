-- +goose Up
-- Server-issued profile unlock grants (MEDIASTORM-515). Rows are keyed by a
-- SHA-256 hash of the opaque unlock token and bound to a SHA-256 hash of the
-- account session token that verified the PIN.
CREATE TABLE IF NOT EXISTS profile_unlocks (
    token_hash TEXT PRIMARY KEY,
    session_hash TEXT NOT NULL,
    account_id TEXT NOT NULL DEFAULT '',
    profile_id TEXT NOT NULL,
    pin_fingerprint TEXT NOT NULL DEFAULT '',
    session_bound BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_profile_unlocks_session ON profile_unlocks(session_hash);
CREATE INDEX IF NOT EXISTS idx_profile_unlocks_profile ON profile_unlocks(profile_id);

-- +goose Down
DROP TABLE IF EXISTS profile_unlocks;
