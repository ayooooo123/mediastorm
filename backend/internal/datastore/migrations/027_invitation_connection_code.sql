-- +goose Up
ALTER TABLE invitations
    ADD COLUMN IF NOT EXISTS remote_access_invite_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS connection_code TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE invitations
    DROP COLUMN IF EXISTS connection_code,
    DROP COLUMN IF EXISTS remote_access_invite_id;
