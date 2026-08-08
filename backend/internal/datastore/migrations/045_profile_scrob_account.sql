-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS scrob_account_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS scrob_account_id;
