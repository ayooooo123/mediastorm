-- New profiles share watch activity anonymously unless explicitly changed.
-- +goose Up
ALTER TABLE users
    ALTER COLUMN activity_privacy SET DEFAULT 'shared_anonymous';

-- Existing choices are preserved.

-- +goose Down
ALTER TABLE users
    ALTER COLUMN activity_privacy SET DEFAULT 'not_shared';
