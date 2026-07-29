-- +goose Up
CREATE TABLE notification_progress_messages (
    channel_id TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    playback_key TEXT NOT NULL,
    message_id TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, playback_key)
);
CREATE INDEX idx_notification_progress_messages_updated_at
    ON notification_progress_messages(updated_at);
CREATE INDEX idx_notification_progress_messages_playback
    ON notification_progress_messages(profile_id, playback_key);

-- +goose Down
DROP TABLE IF EXISTS notification_progress_messages;
