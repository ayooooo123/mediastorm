-- Keep the room clock stopped after the host starts until every joined player
-- has loaded enough to participate in synchronized playback.
-- +goose Up
ALTER TABLE watch_rooms
    ADD COLUMN IF NOT EXISTS waiting_for_ready BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE watch_rooms DROP COLUMN IF EXISTS waiting_for_ready;
