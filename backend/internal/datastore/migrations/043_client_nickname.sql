-- +goose Up
ALTER TABLE clients ADD COLUMN nickname TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE clients DROP COLUMN nickname;
