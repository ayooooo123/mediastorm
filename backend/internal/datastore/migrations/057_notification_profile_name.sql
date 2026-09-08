-- +goose Up
ALTER TABLE notification_channels ADD COLUMN include_profile_name BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE notification_channels DROP COLUMN include_profile_name;
