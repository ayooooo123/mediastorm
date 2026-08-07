-- +goose Up
-- Multi-person device associations and per-(device, person) settings.
-- A physical device (clients.id) may be seen under multiple profiles; device-scoped
-- overrides are keyed by (client_id, user_id) so each person keeps distinct settings.

CREATE TABLE client_profiles (
    client_id TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (client_id, user_id)
);
CREATE INDEX idx_client_profiles_user_id ON client_profiles(user_id);

-- Seed associations from the historical exclusive clients.user_id assignment.
INSERT INTO client_profiles (client_id, user_id, first_seen_at, last_seen_at)
SELECT c.id, c.user_id, c.first_seen_at, c.last_seen_at
FROM clients c
ON CONFLICT DO NOTHING;

-- Rebuild client_settings with composite primary key (client_id, user_id).
CREATE TABLE client_settings_new (
    client_id TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    settings JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (client_id, user_id)
);

-- Existing device-only settings attach to the device's last-known profile.
INSERT INTO client_settings_new (client_id, user_id, settings, updated_at)
SELECT cs.client_id, c.user_id, cs.settings, cs.updated_at
FROM client_settings cs
JOIN clients c ON c.id = cs.client_id
ON CONFLICT DO NOTHING;

DROP TABLE client_settings;
ALTER TABLE client_settings_new RENAME TO client_settings;
CREATE INDEX idx_client_settings_user_id ON client_settings(user_id);

-- +goose Down
CREATE TABLE client_settings_old (
    client_id TEXT PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    settings JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Collapse multi-profile settings: keep the row for the client's last-active user,
-- falling back to any remaining row for that device.
INSERT INTO client_settings_old (client_id, settings, updated_at)
SELECT DISTINCT ON (cs.client_id)
    cs.client_id,
    cs.settings,
    cs.updated_at
FROM client_settings cs
JOIN clients c ON c.id = cs.client_id
ORDER BY cs.client_id,
         CASE WHEN cs.user_id = c.user_id THEN 0 ELSE 1 END,
         cs.updated_at DESC;

DROP TABLE client_settings;
ALTER TABLE client_settings_old RENAME TO client_settings;

DROP TABLE IF EXISTS client_profiles;
