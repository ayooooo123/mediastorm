-- +goose Up
-- +goose StatementBegin
CREATE TABLE remote_media_libraries (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    library_type TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('plex', 'jellyfin')),
    account_id TEXT NOT NULL,
    server_id TEXT NOT NULL DEFAULT '',
    server_name TEXT NOT NULL DEFAULT '',
    external_library_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_sync_started_at TIMESTAMPTZ,
    last_sync_finished_at TIMESTAMPTZ,
    last_sync_status TEXT NOT NULL DEFAULT 'idle',
    last_sync_error TEXT NOT NULL DEFAULT '',
    last_sync_total INTEGER NOT NULL DEFAULT 0,
    UNIQUE (provider, account_id, server_id, external_library_id)
);

CREATE TABLE remote_media_items (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES remote_media_libraries(id) ON DELETE CASCADE,
    external_item_id TEXT NOT NULL,
    external_media_id TEXT NOT NULL DEFAULT '',
    group_key TEXT NOT NULL,
    library_type TEXT NOT NULL,
    title TEXT NOT NULL,
    year INTEGER NOT NULL DEFAULT 0,
    overview TEXT NOT NULL DEFAULT '',
    certification TEXT NOT NULL DEFAULT '',
    season_number INTEGER NOT NULL DEFAULT 0,
    episode_number INTEGER NOT NULL DEFAULT 0,
    episode_title TEXT NOT NULL DEFAULT '',
    external_ids JSONB NOT NULL DEFAULT 'null',
    poster_url TEXT NOT NULL DEFAULT '',
    backdrop_url TEXT NOT NULL DEFAULT '',
    episode_image_url TEXT NOT NULL DEFAULT '',
    file_name TEXT NOT NULL DEFAULT '',
    version_label TEXT NOT NULL DEFAULT '',
    container TEXT NOT NULL DEFAULT '',
    video_codec TEXT NOT NULL DEFAULT '',
    audio_codec TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    hdr_format TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    stream_path TEXT NOT NULL DEFAULT '',
    provider_data JSONB NOT NULL DEFAULT '{}',
    last_seen_sync_id TEXT NOT NULL DEFAULT '',
    is_missing BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (library_id, external_item_id, external_media_id)
);

CREATE INDEX idx_remote_media_items_library ON remote_media_items(library_id, is_missing);
CREATE INDEX idx_remote_media_items_group ON remote_media_items(library_id, group_key);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS remote_media_items;
DROP TABLE IF EXISTS remote_media_libraries;
-- +goose StatementEnd
