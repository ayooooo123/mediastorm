-- +goose Up
-- Per-user, per-series episode ordering override. When a user chooses a
-- non-official TVDB ordering (dvd/absolute/alternate/regional), scrobbling and
-- external watch-sync are disabled for that series because season/episode
-- numbers no longer match the canonical aired ordering used by Trakt/Simkl/MDBList.
CREATE TABLE IF NOT EXISTS series_ordering (
    user_id         TEXT        NOT NULL,
    series_tvdb_id  BIGINT      NOT NULL,
    season_type     TEXT        NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, series_tvdb_id)
);

-- +goose Down
DROP TABLE IF EXISTS series_ordering;
