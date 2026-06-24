-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS allow_share_links BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE accounts
    DROP COLUMN IF EXISTS allow_share_links;

-- +goose Down
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS allow_share_links BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE users
    DROP COLUMN IF EXISTS allow_share_links;
