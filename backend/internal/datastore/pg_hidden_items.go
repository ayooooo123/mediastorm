package datastore

import (
	"context"
	"encoding/json"
	"fmt"

	"novastream/models"
)

type pgHiddenItemsRepo struct {
	pool DB
}

func (r *pgHiddenItemsRepo) ListByUser(ctx context.Context, userID string) ([]models.HiddenItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT media_type, item_id, name, year, poster_url, backdrop_url, external_ids, hidden_at
		FROM hidden_items WHERE user_id = $1 ORDER BY hidden_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list hidden items: %w", err)
	}
	defer rows.Close()

	items := make([]models.HiddenItem, 0)
	for rows.Next() {
		var item models.HiddenItem
		var idsJSON []byte
		if err := rows.Scan(&item.MediaType, &item.ID, &item.Name, &item.Year, &item.PosterURL,
			&item.BackdropURL, &idsJSON, &item.HiddenAt); err != nil {
			return nil, fmt.Errorf("scan hidden item: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *pgHiddenItemsRepo) ListAll(ctx context.Context) (map[string][]models.HiddenItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, media_type, item_id, name, year, poster_url, backdrop_url, external_ids, hidden_at
		FROM hidden_items ORDER BY user_id, hidden_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all hidden items: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.HiddenItem)
	for rows.Next() {
		var userID string
		var item models.HiddenItem
		var idsJSON []byte
		if err := rows.Scan(&userID, &item.MediaType, &item.ID, &item.Name, &item.Year, &item.PosterURL,
			&item.BackdropURL, &idsJSON, &item.HiddenAt); err != nil {
			return nil, fmt.Errorf("scan hidden item: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
		result[userID] = append(result[userID], item)
	}
	return result, rows.Err()
}

func (r *pgHiddenItemsRepo) Upsert(ctx context.Context, userID string, item *models.HiddenItem) error {
	idsJSON, _ := json.Marshal(item.ExternalIDs)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO hidden_items (user_id, item_key, media_type, item_id, name, year, poster_url, backdrop_url, external_ids, hidden_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (user_id, item_key) DO UPDATE SET
		media_type=$3, item_id=$4, name=$5, year=$6, poster_url=$7, backdrop_url=$8, external_ids=$9, hidden_at=$10`,
		userID, item.Key(), item.MediaType, item.ID, item.Name, item.Year, item.PosterURL,
		item.BackdropURL, idsJSON, item.HiddenAt)
	if err != nil {
		return fmt.Errorf("upsert hidden item: %w", err)
	}
	return nil
}

func (r *pgHiddenItemsRepo) Delete(ctx context.Context, userID, itemKey string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM hidden_items WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return err
}

func (r *pgHiddenItemsRepo) DeleteByUser(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM hidden_items WHERE user_id = $1`, userID)
	return err
}

func (r *pgHiddenItemsRepo) BulkUpsert(ctx context.Context, userID string, items []models.HiddenItem) error {
	for _, item := range items {
		if err := r.Upsert(ctx, userID, &item); err != nil {
			return err
		}
	}
	return nil
}

func (r *pgHiddenItemsRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM hidden_items`).Scan(&count)
	return count, err
}
