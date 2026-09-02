package datastore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgRemoteAccessPairingRepo struct {
	pool DB
}

const remoteAccessPairingCols = `id, invite_id, peer_id, credential_hash, peer_name, created_by, created_at, last_authenticated_at, revoked_at`

func (r *pgRemoteAccessPairingRepo) Get(ctx context.Context, id string) (*models.RemoteAccessPairing, error) {
	return scanRemoteAccessPairing(r.pool.QueryRow(ctx, `SELECT `+remoteAccessPairingCols+` FROM remote_access_pairings WHERE id=$1`, id))
}

func (r *pgRemoteAccessPairingRepo) GetByPeerID(ctx context.Context, peerID string) (*models.RemoteAccessPairing, error) {
	return scanRemoteAccessPairing(r.pool.QueryRow(ctx, `SELECT `+remoteAccessPairingCols+` FROM remote_access_pairings WHERE peer_id=$1`, peerID))
}

func (r *pgRemoteAccessPairingRepo) List(ctx context.Context) ([]models.RemoteAccessPairing, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+remoteAccessPairingCols+` FROM remote_access_pairings ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list remote access pairings: %w", err)
	}
	defer rows.Close()
	var result []models.RemoteAccessPairing
	for rows.Next() {
		pairing, err := scanRemoteAccessPairing(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *pairing)
	}
	return result, rows.Err()
}

func (r *pgRemoteAccessPairingRepo) Create(ctx context.Context, pairing *models.RemoteAccessPairing) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO remote_access_pairings (`+remoteAccessPairingCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		pairing.ID, pairing.InviteID, pairing.PeerID, pairing.CredentialHash, pairing.PeerName, pairing.CreatedBy,
		pairing.CreatedAt, pairing.LastAuthenticatedAt, pairing.RevokedAt)
	if err != nil {
		return fmt.Errorf("create remote access pairing: %w", err)
	}
	return nil
}

func (r *pgRemoteAccessPairingRepo) Update(ctx context.Context, pairing *models.RemoteAccessPairing) error {
	_, err := r.pool.Exec(ctx, `UPDATE remote_access_pairings SET invite_id=$2, peer_id=$3, credential_hash=$4, peer_name=$5, created_by=$6, last_authenticated_at=$7, revoked_at=$8 WHERE id=$1`,
		pairing.ID, pairing.InviteID, pairing.PeerID, pairing.CredentialHash, pairing.PeerName, pairing.CreatedBy,
		pairing.LastAuthenticatedAt, pairing.RevokedAt)
	if err != nil {
		return fmt.Errorf("update remote access pairing: %w", err)
	}
	return nil
}

func (r *pgRemoteAccessPairingRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM remote_access_pairings WHERE id=$1`, id)
	return err
}

func (r *pgRemoteAccessPairingRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM remote_access_pairings`).Scan(&count)
	return count, err
}

func (r *pgRemoteAccessPairingRepo) ClaimInvite(
	ctx context.Context,
	tokenHash, peerID, credentialHash, pairingID string,
	now time.Time,
) (*models.RemoteAccessInvite, error) {
	row := r.pool.QueryRow(ctx, `
		WITH claimed AS (
			UPDATE remote_access_invites
			SET used_at=$5,
				used_by_peer_id=$2,
				token_hash='claimed:' || id,
				connection_code=''
			WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>$5 AND used_at IS NULL
			RETURNING `+remoteAccessInviteCols+`
		), paired AS (
			INSERT INTO remote_access_pairings (
				id, invite_id, peer_id, credential_hash, peer_name, created_by, created_at,
				last_authenticated_at, revoked_at
			)
			SELECT $4, id, $2, $3, peer_name, created_by, $5, NULL, NULL FROM claimed
			ON CONFLICT (peer_id) DO UPDATE SET
				invite_id=EXCLUDED.invite_id,
				credential_hash=EXCLUDED.credential_hash,
				peer_name=EXCLUDED.peer_name,
				created_by=EXCLUDED.created_by,
				created_at=EXCLUDED.created_at,
				last_authenticated_at=NULL,
				revoked_at=NULL
			RETURNING peer_id
		), account_invite_scrub AS (
			UPDATE invitations
			SET connection_code=''
			FROM claimed
			WHERE invitations.remote_access_invite_id=claimed.id
		)
		SELECT claimed.`+strings.ReplaceAll(remoteAccessInviteCols, ", ", ", claimed.")+`
		FROM claimed
		JOIN paired ON paired.peer_id=$2`, tokenHash, peerID, credentialHash, pairingID, now)
	return scanRemoteAccessInvite(row)
}

func scanRemoteAccessPairing(row pgx.Row) (*models.RemoteAccessPairing, error) {
	var pairing models.RemoteAccessPairing
	err := row.Scan(&pairing.ID, &pairing.InviteID, &pairing.PeerID, &pairing.CredentialHash, &pairing.PeerName,
		&pairing.CreatedBy, &pairing.CreatedAt, &pairing.LastAuthenticatedAt, &pairing.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan remote access pairing: %w", err)
	}
	return &pairing, nil
}
