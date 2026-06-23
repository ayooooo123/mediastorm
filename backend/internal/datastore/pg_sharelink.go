package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgShareLinkRepo struct {
	pool DB
}

const shareCols = `token, account_id, is_master, label, params, max_uses, use_count, active, created_at, expires_at, last_used_at`

func (r *pgShareLinkRepo) Create(ctx context.Context, link *models.ShareLink) error {
	params, err := json.Marshal(link.Params)
	if err != nil {
		return fmt.Errorf("marshal share params: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO share_links (`+shareCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		link.Token, link.AccountID, link.IsMaster, link.Label, params,
		link.MaxUses, link.UseCount, link.Active, link.CreatedAt, link.ExpiresAt, link.LastUsedAt)
	if err != nil {
		return fmt.Errorf("create share link: %w", err)
	}
	return nil
}

func (r *pgShareLinkRepo) Get(ctx context.Context, token string) (*models.ShareLink, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+shareCols+` FROM share_links WHERE token = $1`, token)
	return scanShareLink(row)
}

func (r *pgShareLinkRepo) ListAll(ctx context.Context) ([]models.ShareLink, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+shareCols+` FROM share_links ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list share links: %w", err)
	}
	defer rows.Close()

	var result []models.ShareLink
	for rows.Next() {
		link, err := scanShareLinkRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *link)
	}
	return result, rows.Err()
}

func (r *pgShareLinkRepo) ConsumeUse(ctx context.Context, token string, now time.Time) (*models.ShareLink, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE share_links
		SET use_count = use_count + 1, last_used_at = $2
		WHERE token = $1 AND active AND expires_at > $2 AND (max_uses = 0 OR use_count < max_uses)
		RETURNING `+shareCols, token, now)
	link, err := scanShareLink(row)
	if err != nil {
		return nil, err
	}
	return link, nil
}

func (r *pgShareLinkRepo) SetActive(ctx context.Context, token string, active bool) error {
	_, err := r.pool.Exec(ctx, `UPDATE share_links SET active = $2 WHERE token = $1`, token, active)
	if err != nil {
		return fmt.Errorf("set share link active: %w", err)
	}
	return nil
}

func (r *pgShareLinkRepo) Delete(ctx context.Context, token string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM share_links WHERE token = $1`, token)
	return err
}

func (r *pgShareLinkRepo) DeleteExpired(ctx context.Context, now time.Time) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM share_links WHERE expires_at <= $1`, now)
	return err
}

func scanShareLink(row pgx.Row) (*models.ShareLink, error) {
	link, err := scanShareLinkRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return link, err
}

func scanShareLinkRow(row pgx.Row) (*models.ShareLink, error) {
	var link models.ShareLink
	var params []byte
	if err := row.Scan(&link.Token, &link.AccountID, &link.IsMaster, &link.Label, &params,
		&link.MaxUses, &link.UseCount, &link.Active, &link.CreatedAt, &link.ExpiresAt, &link.LastUsedAt); err != nil {
		return nil, err
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &link.Params); err != nil {
			return nil, fmt.Errorf("unmarshal share params: %w", err)
		}
	}
	if link.Params == nil {
		link.Params = map[string]string{}
	}
	return &link, nil
}
