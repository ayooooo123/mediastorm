-- +goose Up
CREATE TABLE notification_channels (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    url TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    events JSONB NOT NULL DEFAULT '[]',
    notify_watchlist BOOLEAN NOT NULL DEFAULT true,
    notify_trending BOOLEAN NOT NULL DEFAULT false,
    trending_limit INTEGER NOT NULL DEFAULT 20,
    title_template TEXT NOT NULL DEFAULT '{{eventLabel}}: {{title}}',
    body_template TEXT NOT NULL DEFAULT '{{mediaLabel}}{{progressLabel}}{{releaseLabel}}',
    include_poster BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_notification_channels_profile_id ON notification_channels(profile_id);

CREATE TABLE notification_observations (
    profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    status TEXT NOT NULL,
    event JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (profile_id, item_key)
);

-- +goose Down
DROP TABLE IF EXISTS notification_observations;
DROP TABLE IF EXISTS notification_channels;
