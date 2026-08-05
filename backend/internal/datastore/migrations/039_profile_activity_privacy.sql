-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS activity_privacy TEXT NOT NULL DEFAULT 'not_shared';

UPDATE users
SET activity_privacy = 'not_shared'
WHERE activity_privacy NOT IN ('not_shared', 'shared_anonymous', 'shared');

ALTER TABLE users
    ADD CONSTRAINT users_activity_privacy_check
    CHECK (activity_privacy IN ('not_shared', 'shared_anonymous', 'shared'));

-- +goose Down
ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_activity_privacy_check;

ALTER TABLE users
    DROP COLUMN IF EXISTS activity_privacy;
