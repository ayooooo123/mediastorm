-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS pin_length INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users
    DROP COLUMN IF EXISTS pin_length;
