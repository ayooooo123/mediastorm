package datastore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgRemoteMediaRepo struct{ pool DB }

const remoteLibraryColumns = `id, name, library_type, provider, account_id, server_id, server_name, server_url,
	external_library_id, created_at, updated_at, last_sync_started_at, last_sync_finished_at,
	last_sync_status, last_sync_error, last_sync_total`

func scanRemoteLibrary(row interface{ Scan(...interface{}) error }) (*models.RemoteMediaLibrary, error) {
	var v models.RemoteMediaLibrary
	err := row.Scan(&v.ID, &v.Name, &v.Type, &v.Provider, &v.AccountID, &v.ServerID, &v.ServerName, &v.ServerURL,
		&v.ExternalLibraryID, &v.CreatedAt, &v.UpdatedAt, &v.LastSyncStartedAt, &v.LastSyncFinishedAt,
		&v.LastSyncStatus, &v.LastSyncError, &v.LastSyncTotal)
	return &v, err
}

func (r *pgRemoteMediaRepo) ListLibraries(ctx context.Context) ([]models.RemoteMediaLibrary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+remoteLibraryColumns+` FROM remote_media_libraries ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list remote media libraries: %w", err)
	}
	defer rows.Close()
	result := []models.RemoteMediaLibrary{}
	for rows.Next() {
		v, err := scanRemoteLibrary(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *v)
	}
	return result, rows.Err()
}

func (r *pgRemoteMediaRepo) GetLibrary(ctx context.Context, id string) (*models.RemoteMediaLibrary, error) {
	v, err := scanRemoteLibrary(r.pool.QueryRow(ctx, `SELECT `+remoteLibraryColumns+` FROM remote_media_libraries WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get remote media library: %w", err)
	}
	return v, nil
}

func (r *pgRemoteMediaRepo) CreateLibrary(ctx context.Context, v *models.RemoteMediaLibrary) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO remote_media_libraries (`+remoteLibraryColumns+`) VALUES
		($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, v.ID, v.Name, v.Type, v.Provider,
		v.AccountID, v.ServerID, v.ServerName, v.ServerURL, v.ExternalLibraryID, v.CreatedAt, v.UpdatedAt,
		v.LastSyncStartedAt, v.LastSyncFinishedAt, v.LastSyncStatus, v.LastSyncError, v.LastSyncTotal)
	if err != nil {
		return fmt.Errorf("create remote media library: %w", err)
	}
	return nil
}

func (r *pgRemoteMediaRepo) UpdateLibrary(ctx context.Context, v *models.RemoteMediaLibrary) error {
	_, err := r.pool.Exec(ctx, `UPDATE remote_media_libraries SET name=$2, library_type=$3, server_name=$4,
		server_url=$5, updated_at=$6, last_sync_started_at=$7, last_sync_finished_at=$8, last_sync_status=$9,
		last_sync_error=$10, last_sync_total=$11 WHERE id=$1`, v.ID, v.Name, v.Type, v.ServerName, v.ServerURL,
		v.UpdatedAt, v.LastSyncStartedAt, v.LastSyncFinishedAt, v.LastSyncStatus, v.LastSyncError, v.LastSyncTotal)
	if err != nil {
		return fmt.Errorf("update remote media library: %w", err)
	}
	return nil
}

func (r *pgRemoteMediaRepo) DeleteLibrary(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM remote_media_libraries WHERE id=$1`, id)
	return err
}

const remoteItemColumns = `id, library_id, external_item_id, external_media_id, group_key, library_type,
	title, year, overview, certification, season_number, episode_number, episode_title, external_ids,
	poster_url, backdrop_url, episode_image_url, file_name, version_label, container, video_codec,
	audio_codec, width, height, hdr_format, size_bytes, stream_path, provider_data, last_seen_sync_id,
	is_missing, created_at, updated_at, duration_seconds`

func scanRemoteItem(row interface{ Scan(...interface{}) error }) (*models.RemoteMediaItem, error) {
	var v models.RemoteMediaItem
	var externalIDs, providerData []byte
	err := row.Scan(&v.ID, &v.LibraryID, &v.ExternalItemID, &v.ExternalMediaID, &v.GroupKey, &v.LibraryType,
		&v.Title, &v.Year, &v.Overview, &v.Certification, &v.SeasonNumber, &v.EpisodeNumber, &v.EpisodeTitle,
		&externalIDs, &v.PosterURL, &v.BackdropURL, &v.EpisodeImageURL, &v.FileName, &v.VersionLabel,
		&v.Container, &v.VideoCodec, &v.AudioCodec, &v.Width, &v.Height, &v.HDRFormat, &v.SizeBytes,
		&v.StreamPath, &providerData, &v.LastSeenSyncID, &v.IsMissing, &v.CreatedAt, &v.UpdatedAt, &v.DurationSeconds)
	if err != nil {
		return nil, err
	}
	if string(externalIDs) != "null" {
		_ = json.Unmarshal(externalIDs, &v.ExternalIDs)
	}
	_ = json.Unmarshal(providerData, &v.ProviderData)
	return &v, nil
}

func (r *pgRemoteMediaRepo) ListItems(ctx context.Context, libraryID string, includeMissing bool) ([]models.RemoteMediaItem, error) {
	query := `SELECT ` + remoteItemColumns + ` FROM remote_media_items WHERE library_id=$1`
	if !includeMissing {
		query += ` AND is_missing=FALSE`
	}
	query += ` ORDER BY title, season_number, episode_number, version_label`
	rows, err := r.pool.Query(ctx, query, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []models.RemoteMediaItem{}
	for rows.Next() {
		v, err := scanRemoteItem(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *v)
	}
	return result, rows.Err()
}

func (r *pgRemoteMediaRepo) GetItem(ctx context.Context, id string) (*models.RemoteMediaItem, error) {
	v, err := scanRemoteItem(r.pool.QueryRow(ctx, `SELECT `+remoteItemColumns+` FROM remote_media_items WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func (r *pgRemoteMediaRepo) UpsertItem(ctx context.Context, v *models.RemoteMediaItem) error {
	externalIDs, _ := json.Marshal(v.ExternalIDs)
	// Never store JSON null for provider_data; nil maps marshal to null and wipe partKeys on restore.
	providerDataMap := v.ProviderData
	if providerDataMap == nil {
		providerDataMap = map[string]string{}
	}
	providerData, _ := json.Marshal(providerDataMap)
	_, err := r.pool.Exec(ctx, `INSERT INTO remote_media_items (`+remoteItemColumns+`) VALUES
		($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33)
		ON CONFLICT (library_id, external_item_id, external_media_id) DO UPDATE SET id=EXCLUDED.id, group_key=EXCLUDED.group_key,
		library_type=EXCLUDED.library_type, title=EXCLUDED.title, year=EXCLUDED.year, overview=EXCLUDED.overview,
		certification=EXCLUDED.certification, season_number=EXCLUDED.season_number, episode_number=EXCLUDED.episode_number,
		episode_title=EXCLUDED.episode_title, external_ids=EXCLUDED.external_ids, poster_url=EXCLUDED.poster_url,
		backdrop_url=EXCLUDED.backdrop_url, episode_image_url=EXCLUDED.episode_image_url, file_name=EXCLUDED.file_name,
		version_label=EXCLUDED.version_label, container=EXCLUDED.container, video_codec=EXCLUDED.video_codec,
		audio_codec=EXCLUDED.audio_codec, width=EXCLUDED.width, height=EXCLUDED.height, hdr_format=EXCLUDED.hdr_format,
		size_bytes=EXCLUDED.size_bytes, stream_path=EXCLUDED.stream_path, provider_data=EXCLUDED.provider_data,
		last_seen_sync_id=EXCLUDED.last_seen_sync_id, is_missing=FALSE,
		created_at=LEAST(remote_media_items.created_at, EXCLUDED.created_at), updated_at=EXCLUDED.updated_at,
		duration_seconds=EXCLUDED.duration_seconds`,
		v.ID, v.LibraryID, v.ExternalItemID, v.ExternalMediaID, v.GroupKey, v.LibraryType, v.Title, v.Year,
		v.Overview, v.Certification, v.SeasonNumber, v.EpisodeNumber, v.EpisodeTitle, externalIDs, v.PosterURL,
		v.BackdropURL, v.EpisodeImageURL, v.FileName, v.VersionLabel, v.Container, v.VideoCodec, v.AudioCodec,
		v.Width, v.Height, v.HDRFormat, v.SizeBytes, v.StreamPath, providerData, v.LastSeenSyncID, v.IsMissing,
		v.CreatedAt, v.UpdatedAt, v.DurationSeconds)
	return err
}

func (r *pgRemoteMediaRepo) MarkItemsMissingNotSeenInSync(ctx context.Context, libraryID, syncID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE remote_media_items SET is_missing=TRUE, updated_at=now()
		WHERE library_id=$1 AND last_seen_sync_id<>$2`, libraryID, syncID)
	return err
}
