-- +goose Up
-- Last built calendar per profile (kind = 'full' or 'lite'), so a backend
-- restart can serve Home/Calendar immediately instead of waiting minutes for
-- a cold rebuild. body is a gzip'd JSON array of calendar items.
CREATE TABLE IF NOT EXISTS user_calendars (
    user_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    body BYTEA NOT NULL,
    item_count INTEGER NOT NULL DEFAULT 0,
    refreshed_at TIMESTAMPTZ NOT NULL,
    stale BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_id, kind)
);

-- +goose Down
DROP TABLE IF EXISTS user_calendars;
