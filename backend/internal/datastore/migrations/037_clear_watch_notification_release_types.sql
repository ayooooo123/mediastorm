-- +goose Up
UPDATE notification_channels
SET release_types = '[]'
WHERE NOT (events ? 'release.available');

-- +goose Down
UPDATE notification_channels
SET release_types = '["digital", "physical"]'
WHERE NOT (events ? 'release.available');
