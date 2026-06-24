-- +goose Up
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS allow_share_links BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE accounts
    DROP COLUMN IF EXISTS allow_share_links;
