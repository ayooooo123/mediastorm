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

type pgWatchRoomRepo struct{ pool DB }

const watchRoomCols = `r.id, r.creator_profile_id, creator.name, r.title, r.media_type, r.item_id,
	r.poster_url, r.backdrop_url, r.params, r.status, r.position, r.duration, r.revision,
	r.updated_by, r.anchor_updated_at, r.created_at, r.expires_at`

func (r *pgWatchRoomRepo) Create(ctx context.Context, room *models.WatchRoom, invitees []string, clientID string) error {
	params, err := json.Marshal(room.Params)
	if err != nil {
		return fmt.Errorf("marshal watch room params: %w", err)
	}
	tx, err := beginDBTx(ctx, r.pool)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO watch_rooms
		(id, creator_profile_id, title, media_type, item_id, poster_url, backdrop_url, params, status,
		 position, duration, revision, updated_by, anchor_updated_at, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		room.ID, room.CreatorProfileID, room.Title, room.MediaType, room.ItemID, room.PosterURL,
		room.BackdropURL, params, room.Status, room.Position, room.Duration, room.Revision,
		room.CreatorProfileID, room.AnchorUpdatedAt, room.CreatedAt, room.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create watch room: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO watch_room_members
		(room_id, profile_id, client_id, is_creator, ready, joined_at, last_seen_at)
		VALUES ($1,$2,$3,true,true,$4,$4)`, room.ID, room.CreatorProfileID, clientID, room.CreatedAt)
	if err != nil {
		return fmt.Errorf("add room creator: %w", err)
	}
	for _, profileID := range invitees {
		if profileID == room.CreatorProfileID {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO watch_room_invites (room_id, profile_id, invited_at)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, room.ID, profileID, room.CreatedAt); err != nil {
			return fmt.Errorf("create room invitation: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// beginDBTx is limited to repositories backed by pgx pool/transaction.
func beginDBTx(ctx context.Context, db DB) (pgx.Tx, error) {
	type beginner interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	b, ok := db.(beginner)
	if !ok {
		return nil, errors.New("watch room transaction unavailable")
	}
	return b.Begin(ctx)
}

func (r *pgWatchRoomRepo) Get(ctx context.Context, roomID string) (*models.WatchRoom, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+watchRoomCols+` FROM watch_rooms r JOIN users creator ON creator.id=r.creator_profile_id WHERE r.id=$1`, roomID)
	room, err := scanWatchRoom(row)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, nil
	}
	room.Members, err = r.listMembers(ctx, roomID)
	return room, err
}

func (r *pgWatchRoomRepo) ListInvitations(ctx context.Context, profileID string, now time.Time) ([]models.WatchRoom, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+watchRoomCols+` FROM watch_rooms r
		JOIN users creator ON creator.id=r.creator_profile_id
		JOIN watch_room_invites i ON i.room_id=r.id AND i.profile_id=$1
		WHERE r.expires_at>$2 AND r.status<>'ended' ORDER BY r.created_at DESC`, profileID, now)
	if err != nil {
		return nil, fmt.Errorf("list watch room invitations: %w", err)
	}
	defer rows.Close()
	rooms := make([]models.WatchRoom, 0)
	for rows.Next() {
		room, err := scanWatchRoomRow(rows)
		if err != nil {
			return nil, err
		}
		room.Members, err = r.listMembers(ctx, room.ID)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, *room)
	}
	return rooms, rows.Err()
}

func (r *pgWatchRoomRepo) IsInvited(ctx context.Context, roomID, profileID string) (bool, error) {
	var allowed bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM watch_rooms r WHERE r.id=$1 AND (r.creator_profile_id=$2 OR EXISTS(
			SELECT 1 FROM watch_room_invites i WHERE i.room_id=r.id AND i.profile_id=$2)))`, roomID, profileID).Scan(&allowed)
	return allowed, err
}

func (r *pgWatchRoomRepo) Join(ctx context.Context, roomID, profileID, clientID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO watch_room_members
		(room_id, profile_id, client_id, joined_at, last_seen_at) VALUES ($1,$2,$3,$4,$4)
		ON CONFLICT (room_id, profile_id) DO UPDATE SET client_id=EXCLUDED.client_id, last_seen_at=EXCLUDED.last_seen_at`, roomID, profileID, clientID, now)
	return err
}

func (r *pgWatchRoomRepo) SetReady(ctx context.Context, roomID, profileID string, ready bool, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE watch_room_members SET ready=$3,last_seen_at=$4 WHERE room_id=$1 AND profile_id=$2`, roomID, profileID, ready, now)
	return err
}

func (r *pgWatchRoomRepo) UpdateState(ctx context.Context, roomID, profileID, status string, position, duration float64, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE watch_rooms SET status=$3,position=$4,duration=GREATEST(duration,$5),
		revision=revision+1,updated_by=$2,anchor_updated_at=$6 WHERE id=$1 AND status<>'ended'`, roomID, profileID, status, position, duration, now)
	return err
}

func (r *pgWatchRoomRepo) Heartbeat(ctx context.Context, roomID, profileID, clientID string, buffering bool, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE watch_room_members SET client_id=$3,buffering=$4,last_seen_at=$5 WHERE room_id=$1 AND profile_id=$2`, roomID, profileID, clientID, buffering, now)
	return err
}

func (r *pgWatchRoomRepo) Leave(ctx context.Context, roomID, profileID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watch_room_members WHERE room_id=$1 AND profile_id=$2 AND NOT is_creator`, roomID, profileID)
	return err
}

func (r *pgWatchRoomRepo) End(ctx context.Context, roomID, profileID string, now time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE watch_rooms SET status='ended',revision=revision+1,updated_by=$2,anchor_updated_at=$3 WHERE id=$1 AND creator_profile_id=$2`, roomID, profileID, now)
	return err == nil && tag.RowsAffected() > 0, err
}

func (r *pgWatchRoomRepo) listMembers(ctx context.Context, roomID string) ([]models.WatchRoomMember, error) {
	rows, err := r.pool.Query(ctx, `WITH people AS (
			SELECT creator_profile_id AS profile_id FROM watch_rooms WHERE id=$1
			UNION
			SELECT profile_id FROM watch_room_invites WHERE room_id=$1
		)
		SELECT p.profile_id,u.name,u.color,u.icon_url,COALESCE(m.client_id,''),
			(p.profile_id=r.creator_profile_id),COALESCE(m.ready,false),COALESCE(m.buffering,false),
			(m.profile_id IS NOT NULL),COALESCE(m.joined_at,r.created_at),
			COALESCE(m.last_seen_at,r.created_at - interval '1 day')
		FROM people p
		JOIN watch_rooms r ON r.id=$1
		JOIN users u ON u.id=p.profile_id
		LEFT JOIN watch_room_members m ON m.room_id=r.id AND m.profile_id=p.profile_id
		ORDER BY (p.profile_id=r.creator_profile_id) DESC,COALESCE(m.joined_at,r.created_at),u.name`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]models.WatchRoomMember, 0)
	for rows.Next() {
		var m models.WatchRoomMember
		if err := rows.Scan(&m.ProfileID, &m.Name, &m.Color, &m.IconURL, &m.ClientID, &m.IsCreator, &m.Ready, &m.Buffering, &m.Joined, &m.JoinedAt, &m.LastSeenAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func scanWatchRoom(row pgx.Row) (*models.WatchRoom, error) {
	room, err := scanWatchRoomRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return room, err
}

func scanWatchRoomRow(row pgx.Row) (*models.WatchRoom, error) {
	var room models.WatchRoom
	var params []byte
	if err := row.Scan(&room.ID, &room.CreatorProfileID, &room.CreatorName, &room.Title, &room.MediaType, &room.ItemID,
		&room.PosterURL, &room.BackdropURL, &params, &room.Status, &room.Position, &room.Duration, &room.Revision,
		&room.UpdatedBy, &room.AnchorUpdatedAt, &room.CreatedAt, &room.ExpiresAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(params, &room.Params); err != nil {
		return nil, err
	}
	if room.Params == nil {
		room.Params = map[string]string{}
	}
	return &room, nil
}
