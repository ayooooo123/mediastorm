-- +goose Up
CREATE TABLE IF NOT EXISTS remote_access_pairings (
    id TEXT PRIMARY KEY,
    invite_id TEXT REFERENCES remote_access_invites(id) ON DELETE SET NULL,
    peer_id TEXT UNIQUE NOT NULL,
    credential_hash TEXT NOT NULL DEFAULT '',
    peer_name TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_authenticated_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_remote_access_pairings_active
    ON remote_access_pairings(peer_id) WHERE revoked_at IS NULL;

-- Existing installations used the app's stable peer ID as the authorization
-- credential. Preserve those pairings as legacy rows; updated clients rotate them to a
-- hashed random credential on their next connection.
INSERT INTO remote_access_pairings (
    id, invite_id, peer_id, credential_hash, peer_name, created_by, created_at, revoked_at
)
SELECT DISTINCT ON (used_by_peer_id)
    'legacy-' || md5(used_by_peer_id),
    id,
    used_by_peer_id,
    '',
    peer_name,
    created_by,
    used_at,
    CASE
        WHEN bool_or(revoked_at IS NULL) OVER (PARTITION BY used_by_peer_id) THEN NULL
        ELSE max(revoked_at) OVER (PARTITION BY used_by_peer_id)
    END
FROM remote_access_invites
WHERE used_at IS NOT NULL AND used_by_peer_id <> ''
ORDER BY used_by_peer_id, (revoked_at IS NULL) DESC, used_at DESC
ON CONFLICT (peer_id) DO NOTHING;

-- A claimed code is no longer a durable credential. Retire both its plaintext form and
-- its reusable digest; paired clients reconnect with the host identity plus pairing secret.
UPDATE remote_access_invites
SET connection_code = '', token_hash = 'claimed:' || id
WHERE used_at IS NOT NULL;

UPDATE invitations
SET connection_code = ''
WHERE remote_access_invite_id IN (
    SELECT id FROM remote_access_invites WHERE used_at IS NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS remote_access_pairings;
