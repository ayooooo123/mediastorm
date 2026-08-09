-- +goose Up
CREATE TABLE watch_rooms (
    id TEXT PRIMARY KEY,
    creator_profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    media_type TEXT NOT NULL,
    item_id TEXT NOT NULL,
    poster_url TEXT NOT NULL DEFAULT '',
    backdrop_url TEXT NOT NULL DEFAULT '',
    params JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'lobby',
    position DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration DOUBLE PRECISION NOT NULL DEFAULT 0,
    revision BIGINT NOT NULL DEFAULT 0,
    updated_by TEXT NOT NULL DEFAULT '',
    anchor_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_watch_rooms_expires_at ON watch_rooms(expires_at);

CREATE TABLE watch_room_invites (
    room_id TEXT NOT NULL REFERENCES watch_rooms(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, profile_id)
);
CREATE INDEX idx_watch_room_invites_profile ON watch_room_invites(profile_id);

CREATE TABLE watch_room_members (
    room_id TEXT NOT NULL REFERENCES watch_rooms(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL DEFAULT '',
    is_creator BOOLEAN NOT NULL DEFAULT false,
    ready BOOLEAN NOT NULL DEFAULT false,
    buffering BOOLEAN NOT NULL DEFAULT false,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, profile_id)
);

-- +goose Down
DROP TABLE IF EXISTS watch_room_members;
DROP TABLE IF EXISTS watch_room_invites;
DROP TABLE IF EXISTS watch_rooms;
