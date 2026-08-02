-- +goose Up
-- +goose StatementBegin
CREATE TABLE media_library_access_policies (
    library_id TEXT PRIMARY KEY,
    access_mode TEXT NOT NULL DEFAULT 'restricted' CHECK (access_mode IN ('all', 'restricted')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE media_library_account_grants (
    library_id TEXT NOT NULL REFERENCES media_library_access_policies(library_id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (library_id, account_id)
);

CREATE TABLE media_library_profile_grants (
    library_id TEXT NOT NULL REFERENCES media_library_access_policies(library_id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (library_id, profile_id)
);

-- Preserve the pre-access-control behaviour for libraries that already exist.
INSERT INTO media_library_access_policies (library_id, access_mode)
SELECT id, 'all' FROM local_media_libraries
UNION
SELECT id, 'all' FROM remote_media_libraries
ON CONFLICT (library_id) DO NOTHING;

CREATE OR REPLACE FUNCTION delete_media_library_access_policy() RETURNS trigger AS $$
BEGIN
    DELETE FROM media_library_access_policies WHERE library_id = OLD.id;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER local_media_library_access_cleanup
AFTER DELETE ON local_media_libraries
FOR EACH ROW EXECUTE FUNCTION delete_media_library_access_policy();

CREATE TRIGGER remote_media_library_access_cleanup
AFTER DELETE ON remote_media_libraries
FOR EACH ROW EXECUTE FUNCTION delete_media_library_access_policy();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS remote_media_library_access_cleanup ON remote_media_libraries;
DROP TRIGGER IF EXISTS local_media_library_access_cleanup ON local_media_libraries;
DROP FUNCTION IF EXISTS delete_media_library_access_policy();
DROP TABLE IF EXISTS media_library_profile_grants;
DROP TABLE IF EXISTS media_library_account_grants;
DROP TABLE IF EXISTS media_library_access_policies;
-- +goose StatementEnd
