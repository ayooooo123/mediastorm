-- +goose Up
ALTER TABLE watchlist ADD COLUMN text_poster_url TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_list_items ADD COLUMN text_poster_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE custom_list_items DROP COLUMN text_poster_url;
ALTER TABLE watchlist DROP COLUMN text_poster_url;
