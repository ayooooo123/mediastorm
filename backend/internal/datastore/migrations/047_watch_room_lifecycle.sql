-- +goose Up
ALTER TABLE watch_rooms
    ADD COLUMN ended_at TIMESTAMPTZ,
    ADD COLUMN end_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_watch_rooms_ended_at ON watch_rooms(ended_at) WHERE ended_at IS NOT NULL;

ALTER TABLE watch_room_members
    ADD COLUMN capabilities JSONB NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE watch_room_members DROP COLUMN IF EXISTS capabilities;
DROP INDEX IF EXISTS idx_watch_rooms_ended_at;
ALTER TABLE watch_rooms
    DROP COLUMN IF EXISTS end_reason,
    DROP COLUMN IF EXISTS ended_at;
