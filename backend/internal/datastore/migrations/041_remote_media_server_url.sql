-- Store the Plex connection that was explicitly verified when the library was added.
-- Existing libraries keep automatic Plex connection selection until reconfigured.
-- +goose Up
ALTER TABLE remote_media_libraries
    ADD COLUMN server_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE remote_media_libraries
    DROP COLUMN server_url;
