package datastore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgSeriesOrderingRepo struct {
	pool DB
}

// Get returns the user's ordering preference for a series, or nil if none set
// (meaning the default/official ordering applies).
func (r *pgSeriesOrderingRepo) Get(ctx context.Context, userID string, seriesTVDBID int64) (*models.SeriesOrderingPref, error) {
	var p models.SeriesOrderingPref
	err := r.pool.QueryRow(ctx, `
		SELECT series_tvdb_id, season_type, updated_at
		FROM series_ordering WHERE user_id = $1 AND series_tvdb_id = $2`, userID, seriesTVDBID).
		Scan(&p.SeriesTVDBID, &p.SeasonType, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get series ordering: %w", err)
	}
	return &p, nil
}

// ListAll returns every series ordering preference keyed by user ID.
func (r *pgSeriesOrderingRepo) ListAll(ctx context.Context) (map[string][]models.SeriesOrderingPref, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, series_tvdb_id, season_type, updated_at
		FROM series_ordering ORDER BY user_id, series_tvdb_id`)
	if err != nil {
		return nil, fmt.Errorf("list all series ordering: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.SeriesOrderingPref)
	for rows.Next() {
		var userID string
		var pref models.SeriesOrderingPref
		if err := rows.Scan(&userID, &pref.SeriesTVDBID, &pref.SeasonType, &pref.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan series ordering: %w", err)
		}
		result[userID] = append(result[userID], pref)
	}
	return result, rows.Err()
}

// Upsert stores the user's chosen ordering for a series.
func (r *pgSeriesOrderingRepo) Upsert(ctx context.Context, userID string, pref *models.SeriesOrderingPref) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO series_ordering (user_id, series_tvdb_id, season_type, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, series_tvdb_id) DO UPDATE SET
		season_type = $3, updated_at = $4`,
		userID, pref.SeriesTVDBID, pref.SeasonType, pref.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert series ordering: %w", err)
	}
	return nil
}

// Delete clears any override, reverting the series to the default ordering.
func (r *pgSeriesOrderingRepo) Delete(ctx context.Context, userID string, seriesTVDBID int64) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM series_ordering WHERE user_id = $1 AND series_tvdb_id = $2`, userID, seriesTVDBID)
	return err
}

// DeleteAll removes every series ordering override (used during backup restore).
func (r *pgSeriesOrderingRepo) DeleteAll(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM series_ordering`)
	if err != nil {
		return fmt.Errorf("delete all series ordering: %w", err)
	}
	return nil
}
