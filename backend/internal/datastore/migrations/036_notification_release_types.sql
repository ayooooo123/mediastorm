-- +goose Up
ALTER TABLE notification_channels
    ADD COLUMN release_types JSONB NOT NULL DEFAULT '["digital", "physical"]';

-- +goose Down
ALTER TABLE notification_channels
    DROP COLUMN IF EXISTS release_types;
