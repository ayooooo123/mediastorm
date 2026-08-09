-- +goose Up
CREATE TABLE watch_room_account_invites (
    id TEXT PRIMARY KEY,
    room_id TEXT NOT NULL REFERENCES watch_rooms(id) ON DELETE CASCADE,
    inviter_account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    invitee_account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    accepted_profile_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    responded_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    UNIQUE (room_id, invitee_account_id)
);

CREATE INDEX idx_watch_room_account_invites_recipient
    ON watch_room_account_invites(invitee_account_id, status, expires_at);

-- +goose Down
DROP TABLE IF EXISTS watch_room_account_invites;
