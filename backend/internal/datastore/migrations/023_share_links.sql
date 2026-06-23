-- +goose Up
CREATE TABLE IF NOT EXISTS share_links (
    token        TEXT PRIMARY KEY,
    account_id   TEXT NOT NULL,
    is_master    BOOLEAN NOT NULL DEFAULT false,
    label        TEXT NOT NULL DEFAULT '',
    params       JSONB NOT NULL DEFAULT '{}',
    max_uses     INTEGER NOT NULL DEFAULT 0,
    use_count    INTEGER NOT NULL DEFAULT 0,
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_share_links_account ON share_links(account_id);
CREATE INDEX IF NOT EXISTS idx_share_links_expires ON share_links(expires_at);

-- +goose Down
DROP TABLE IF EXISTS share_links;
