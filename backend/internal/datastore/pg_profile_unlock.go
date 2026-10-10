package datastore

import (
	"context"
	"fmt"
	"time"

	"novastream/models"
)

type pgProfileUnlockRepo struct {
	pool DB
}

const profileUnlockColumns = `token_hash, session_hash, account_id, profile_id, pin_fingerprint, session_bound, expires_at, created_at`

func (r *pgProfileUnlockRepo) Create(ctx context.Context, unlock *models.ProfileUnlock) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO profile_unlocks (`+profileUnlockColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (token_hash) DO UPDATE SET
			session_hash = EXCLUDED.session_hash,
			account_id = EXCLUDED.account_id,
			profile_id = EXCLUDED.profile_id,
			pin_fingerprint = EXCLUDED.pin_fingerprint,
			session_bound = EXCLUDED.session_bound,
			expires_at = EXCLUDED.expires_at`,
		unlock.TokenHash, unlock.SessionHash, unlock.AccountID, unlock.ProfileID,
		unlock.PinFingerprint, unlock.SessionBound, unlock.ExpiresAt, unlock.CreatedAt)
	if err != nil {
		return fmt.Errorf("create profile unlock: %w", err)
	}
	return nil
}

func (r *pgProfileUnlockRepo) List(ctx context.Context) ([]models.ProfileUnlock, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+profileUnlockColumns+` FROM profile_unlocks WHERE expires_at > $1`, time.Now())
	if err != nil {
		return nil, fmt.Errorf("list profile unlocks: %w", err)
	}
	defer rows.Close()
	var result []models.ProfileUnlock
	for rows.Next() {
		var u models.ProfileUnlock
		if err := rows.Scan(&u.TokenHash, &u.SessionHash, &u.AccountID, &u.ProfileID, &u.PinFingerprint, &u.SessionBound, &u.ExpiresAt, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan profile unlock: %w", err)
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (r *pgProfileUnlockRepo) DeleteBySession(ctx context.Context, sessionHash string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM profile_unlocks WHERE session_hash = $1`, sessionHash); err != nil {
		return fmt.Errorf("delete profile unlocks by session: %w", err)
	}
	return nil
}

func (r *pgProfileUnlockRepo) DeleteByProfile(ctx context.Context, profileID string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM profile_unlocks WHERE profile_id = $1`, profileID); err != nil {
		return fmt.Errorf("delete profile unlocks by profile: %w", err)
	}
	return nil
}

func (r *pgProfileUnlockRepo) RebindSession(ctx context.Context, oldSessionHash, newSessionHash string, sessionExpiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE profile_unlocks
		SET session_hash = $2,
			expires_at = CASE WHEN session_bound THEN $3 ELSE expires_at END
		WHERE session_hash = $1`, oldSessionHash, newSessionHash, sessionExpiresAt)
	if err != nil {
		return fmt.Errorf("rebind profile unlocks: %w", err)
	}
	return nil
}

func (r *pgProfileUnlockRepo) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM profile_unlocks WHERE expires_at <= $1`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired profile unlocks: %w", err)
	}
	return tag.RowsAffected(), nil
}
