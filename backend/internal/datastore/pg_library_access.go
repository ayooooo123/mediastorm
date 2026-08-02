package datastore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgLibraryAccessRepo struct{ pool DB }

func (r *pgLibraryAccessRepo) Get(ctx context.Context, libraryID string) (*models.LibraryAccessPolicy, error) {
	var policy models.LibraryAccessPolicy
	policy.LibraryID = libraryID
	if err := r.pool.QueryRow(ctx, `SELECT access_mode FROM media_library_access_policies WHERE library_id=$1`, libraryID).Scan(&policy.AccessMode); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get library access policy: %w", err)
	}
	accounts, err := r.listGrantIDs(ctx, `SELECT account_id FROM media_library_account_grants WHERE library_id=$1 ORDER BY account_id`, libraryID)
	if err != nil {
		return nil, err
	}
	profiles, err := r.listGrantIDs(ctx, `SELECT profile_id FROM media_library_profile_grants WHERE library_id=$1 ORDER BY profile_id`, libraryID)
	if err != nil {
		return nil, err
	}
	policy.AllowedAccountIDs = accounts
	policy.AllowedProfileIDs = profiles
	return &policy, nil
}

func (r *pgLibraryAccessRepo) List(ctx context.Context) (map[string]models.LibraryAccessPolicy, error) {
	rows, err := r.pool.Query(ctx, `SELECT library_id, access_mode FROM media_library_access_policies ORDER BY library_id`)
	if err != nil {
		return nil, fmt.Errorf("list library access policies: %w", err)
	}
	defer rows.Close()
	result := make(map[string]models.LibraryAccessPolicy)
	for rows.Next() {
		var policy models.LibraryAccessPolicy
		if err := rows.Scan(&policy.LibraryID, &policy.AccessMode); err != nil {
			return nil, err
		}
		policy.AllowedAccountIDs = []string{}
		policy.AllowedProfileIDs = []string{}
		result[policy.LibraryID] = policy
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accountRows, err := r.pool.Query(ctx, `SELECT library_id, account_id FROM media_library_account_grants ORDER BY library_id, account_id`)
	if err != nil {
		return nil, fmt.Errorf("list library account grants: %w", err)
	}
	for accountRows.Next() {
		var libraryID, accountID string
		if err := accountRows.Scan(&libraryID, &accountID); err != nil {
			accountRows.Close()
			return nil, err
		}
		policy := result[libraryID]
		policy.LibraryID = libraryID
		policy.AllowedAccountIDs = append(policy.AllowedAccountIDs, accountID)
		result[libraryID] = policy
	}
	if err := accountRows.Err(); err != nil {
		accountRows.Close()
		return nil, err
	}
	accountRows.Close()
	profileRows, err := r.pool.Query(ctx, `SELECT library_id, profile_id FROM media_library_profile_grants ORDER BY library_id, profile_id`)
	if err != nil {
		return nil, fmt.Errorf("list library profile grants: %w", err)
	}
	defer profileRows.Close()
	for profileRows.Next() {
		var libraryID, profileID string
		if err := profileRows.Scan(&libraryID, &profileID); err != nil {
			return nil, err
		}
		policy := result[libraryID]
		policy.LibraryID = libraryID
		policy.AllowedProfileIDs = append(policy.AllowedProfileIDs, profileID)
		result[libraryID] = policy
	}
	return result, profileRows.Err()
}

func (r *pgLibraryAccessRepo) Set(ctx context.Context, policy models.LibraryAccessPolicy) error {
	mode := policy.AccessMode
	if mode != models.LibraryAccessModeAll {
		mode = models.LibraryAccessModeRestricted
	}
	accountIDs := policy.AllowedAccountIDs
	profileIDs := policy.AllowedProfileIDs
	if mode == models.LibraryAccessModeAll {
		accountIDs = []string{}
		profileIDs = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		WITH saved_policy AS (
			INSERT INTO media_library_access_policies (library_id, access_mode, updated_at)
			VALUES ($1,$2,now())
			ON CONFLICT (library_id) DO UPDATE SET access_mode=EXCLUDED.access_mode, updated_at=now()
			RETURNING library_id
		), inserted_accounts AS (
			INSERT INTO media_library_account_grants (library_id, account_id)
			SELECT saved_policy.library_id, account_id
			FROM saved_policy CROSS JOIN unnest($3::text[]) AS granted_account(account_id)
			ON CONFLICT DO NOTHING
		), deleted_accounts AS (
			DELETE FROM media_library_account_grants
			WHERE library_id=(SELECT library_id FROM saved_policy)
			  AND NOT (account_id=ANY($3::text[]))
		), inserted_profiles AS (
			INSERT INTO media_library_profile_grants (library_id, profile_id)
			SELECT saved_policy.library_id, profile_id
			FROM saved_policy CROSS JOIN unnest($4::text[]) AS granted_profile(profile_id)
			ON CONFLICT DO NOTHING
		)
		DELETE FROM media_library_profile_grants
		WHERE library_id=(SELECT library_id FROM saved_policy)
		  AND NOT (profile_id=ANY($4::text[]))`, policy.LibraryID, mode, accountIDs, profileIDs)
	if err != nil {
		return fmt.Errorf("set library access policy: %w", err)
	}
	return nil
}

func (r *pgLibraryAccessRepo) Delete(ctx context.Context, libraryID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM media_library_access_policies WHERE library_id=$1`, libraryID)
	return err
}

func (r *pgLibraryAccessRepo) listGrantIDs(ctx context.Context, query, libraryID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, query, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
