package datastore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgClientRepo struct {
	pool DB
}

const clientCols = `id, user_id, name, nickname, device_name, device_type, os, app_version, last_seen_at, first_seen_at, filter_enabled`

func (r *pgClientRepo) Get(ctx context.Context, id string) (*models.Client, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+clientCols+` FROM clients WHERE id = $1`, id)
	return scanClient(row)
}

func (r *pgClientRepo) ListByUser(ctx context.Context, userID string) ([]models.Client, error) {
	// Prefer multi-person associations; fall back to exclusive user_id for rows
	// that somehow lack client_profiles (should not happen after migration).
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, cp.user_id, c.name, c.nickname, c.device_name, c.device_type, c.os, c.app_version,
			cp.last_seen_at, cp.first_seen_at, c.filter_enabled
		FROM clients c
		INNER JOIN client_profiles cp ON cp.client_id = c.id
		WHERE cp.user_id = $1
		ORDER BY cp.last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list clients by user: %w", err)
	}
	defer rows.Close()
	return collectClients(rows)
}

func (r *pgClientRepo) ListProfiles(ctx context.Context) ([]models.ClientProfileAssociation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT client_id, user_id, first_seen_at, last_seen_at
		FROM client_profiles
		ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list client profiles: %w", err)
	}
	defer rows.Close()
	return collectClientProfiles(rows)
}

func (r *pgClientRepo) ListProfilesByClient(ctx context.Context, clientID string) ([]models.ClientProfileAssociation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT client_id, user_id, first_seen_at, last_seen_at
		FROM client_profiles
		WHERE client_id = $1
		ORDER BY last_seen_at DESC`, clientID)
	if err != nil {
		return nil, fmt.Errorf("list client profiles by client: %w", err)
	}
	defer rows.Close()
	return collectClientProfiles(rows)
}

func (r *pgClientRepo) UpsertProfile(ctx context.Context, assoc models.ClientProfileAssociation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO client_profiles (client_id, user_id, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (client_id, user_id) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at`,
		assoc.ClientID, assoc.UserID, assoc.FirstSeenAt, assoc.LastSeenAt)
	if err != nil {
		return fmt.Errorf("upsert client profile: %w", err)
	}
	return nil
}

func (r *pgClientRepo) DeleteProfile(ctx context.Context, clientID, userID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM client_profiles WHERE client_id = $1 AND user_id = $2`,
		clientID, userID)
	return err
}

func (r *pgClientRepo) DeleteProfilesByClient(ctx context.Context, clientID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM client_profiles WHERE client_id = $1`, clientID)
	return err
}

func collectClientProfiles(rows pgx.Rows) ([]models.ClientProfileAssociation, error) {
	var result []models.ClientProfileAssociation
	for rows.Next() {
		var a models.ClientProfileAssociation
		if err := rows.Scan(&a.ClientID, &a.UserID, &a.FirstSeenAt, &a.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan client profile: %w", err)
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (r *pgClientRepo) List(ctx context.Context) ([]models.Client, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+clientCols+` FROM clients ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()
	return collectClients(rows)
}

func (r *pgClientRepo) Create(ctx context.Context, c *models.Client) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO clients (`+clientCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		c.ID, c.UserID, c.Name, c.Nickname, c.DeviceName, c.DeviceType, c.OS, c.AppVersion, c.LastSeenAt, c.FirstSeenAt, c.FilterEnabled)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	return nil
}

func (r *pgClientRepo) Update(ctx context.Context, c *models.Client) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE clients SET user_id=$2, name=$3, nickname=$4, device_name=$5, device_type=$6, os=$7, app_version=$8,
		last_seen_at=$9, first_seen_at=$10, filter_enabled=$11
		WHERE id=$1`,
		c.ID, c.UserID, c.Name, c.Nickname, c.DeviceName, c.DeviceType, c.OS, c.AppVersion, c.LastSeenAt, c.FirstSeenAt, c.FilterEnabled)
	if err != nil {
		return fmt.Errorf("update client: %w", err)
	}
	return nil
}

func (r *pgClientRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM clients WHERE id = $1`, id)
	return err
}

func (r *pgClientRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM clients`).Scan(&count)
	return count, err
}

func scanClient(row pgx.Row) (*models.Client, error) {
	var c models.Client
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Nickname, &c.DeviceName, &c.DeviceType, &c.OS, &c.AppVersion, &c.LastSeenAt, &c.FirstSeenAt, &c.FilterEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan client: %w", err)
	}
	return &c, nil
}

func collectClients(rows pgx.Rows) ([]models.Client, error) {
	var result []models.Client
	for rows.Next() {
		var c models.Client
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.Nickname, &c.DeviceName, &c.DeviceType, &c.OS, &c.AppVersion,
			&c.LastSeenAt, &c.FirstSeenAt, &c.FilterEnabled); err != nil {
			return nil, fmt.Errorf("scan client: %w", err)
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
