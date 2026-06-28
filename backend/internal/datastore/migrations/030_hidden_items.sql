-- +goose Up
CREATE TABLE IF NOT EXISTS hidden_items (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    media_type TEXT NOT NULL,
    item_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    poster_url TEXT NOT NULL DEFAULT '',
    backdrop_url TEXT NOT NULL DEFAULT '',
    external_ids JSONB NOT NULL DEFAULT '{}',
    hidden_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, item_key)
);

CREATE INDEX IF NOT EXISTS idx_hidden_items_user_id ON hidden_items(user_id);
CREATE INDEX IF NOT EXISTS idx_hidden_items_hidden_at ON hidden_items(hidden_at);

-- +goose Down
DROP TABLE IF EXISTS hidden_items;
