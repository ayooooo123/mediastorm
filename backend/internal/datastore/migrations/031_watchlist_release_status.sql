-- +goose Up
ALTER TABLE watchlist ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT '';
ALTER TABLE watchlist ADD COLUMN IF NOT EXISTS lifecycle_status TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_list_items ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_list_items ADD COLUMN IF NOT EXISTS lifecycle_status TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE custom_list_items DROP COLUMN IF EXISTS lifecycle_status;
ALTER TABLE custom_list_items DROP COLUMN IF EXISTS status;
ALTER TABLE watchlist DROP COLUMN IF EXISTS lifecycle_status;
ALTER TABLE watchlist DROP COLUMN IF EXISTS status;
