-- +goose Up
-- "watch.playing" was emitted alongside "watch.started" on normal playback.
-- Preserve existing subscriptions by mapping play-only channels to Start, then
-- remove the obsolete event from every channel.
UPDATE notification_channels
SET events = CASE
    WHEN events ? 'watch.started' THEN events - 'watch.playing'
    ELSE (events - 'watch.playing') || '["watch.started"]'::jsonb
END
WHERE events ? 'watch.playing';

-- +goose Down
-- This data cleanup is intentionally irreversible: after migration there is no
-- reliable way to distinguish an original Start subscription from a mapped one.
