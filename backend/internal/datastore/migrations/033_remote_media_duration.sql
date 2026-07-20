-- +goose Up
ALTER TABLE remote_media_items
    ADD COLUMN duration_seconds DOUBLE PRECISION NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE remote_media_items
    DROP COLUMN duration_seconds;
