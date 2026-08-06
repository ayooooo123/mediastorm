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

type pgNotificationRepo struct {
	pool DB
}

func scanNotificationChannel(row pgx.Row) (*models.NotificationChannel, error) {
	var channel models.NotificationChannel
	var eventsJSON []byte
	var releaseTypesJSON []byte
	err := row.Scan(
		&channel.ID, &channel.ProfileID, &channel.Name, &channel.Type, &channel.URL,
		&channel.Enabled, &eventsJSON, &channel.NotifyWatchlist, &channel.NotifyTrending,
		&channel.TrendingLimit, &releaseTypesJSON, &channel.TitleTemplate, &channel.BodyTemplate,
		&channel.IncludePoster, &channel.CreatedAt, &channel.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(eventsJSON, &channel.Events); err != nil {
		return nil, fmt.Errorf("decode notification events: %w", err)
	}
	if err := json.Unmarshal(releaseTypesJSON, &channel.ReleaseTypes); err != nil {
		return nil, fmt.Errorf("decode notification release types: %w", err)
	}
	channel.URLConfigured = channel.URL != ""
	return &channel, nil
}

func (r *pgNotificationRepo) GetChannel(ctx context.Context, id string) (*models.NotificationChannel, error) {
	return scanNotificationChannel(r.pool.QueryRow(ctx, `
		SELECT id, profile_id, name, type, url, enabled, events, notify_watchlist,
		       notify_trending, trending_limit, release_types, title_template, body_template,
		       include_poster, created_at, updated_at
		FROM notification_channels WHERE id = $1`, id))
}

func (r *pgNotificationRepo) ListChannels(ctx context.Context, profileID string) ([]models.NotificationChannel, error) {
	return r.listChannels(ctx, `
		SELECT id, profile_id, name, type, url, enabled, events, notify_watchlist,
		       notify_trending, trending_limit, release_types, title_template, body_template,
		       include_poster, created_at, updated_at
		FROM notification_channels WHERE profile_id = $1 ORDER BY created_at`, profileID)
}

func (r *pgNotificationRepo) ListAllChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	return r.listChannels(ctx, `
		SELECT id, profile_id, name, type, url, enabled, events, notify_watchlist,
		       notify_trending, trending_limit, release_types, title_template, body_template,
		       include_poster, created_at, updated_at
		FROM notification_channels ORDER BY created_at`)
}

func (r *pgNotificationRepo) listChannels(ctx context.Context, query string, args ...any) ([]models.NotificationChannel, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	defer rows.Close()
	channels := make([]models.NotificationChannel, 0)
	for rows.Next() {
		channel, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification channel: %w", err)
		}
		channels = append(channels, *channel)
	}
	return channels, rows.Err()
}

func (r *pgNotificationRepo) CreateChannel(ctx context.Context, channel *models.NotificationChannel) error {
	eventsJSON, _ := json.Marshal(channel.Events)
	releaseTypesJSON, _ := json.Marshal(channel.ReleaseTypes)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_channels (
			id, profile_id, name, type, url, enabled, events, notify_watchlist,
			notify_trending, trending_limit, release_types, title_template, body_template,
			include_poster, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		channel.ID, channel.ProfileID, channel.Name, channel.Type, channel.URL,
		channel.Enabled, eventsJSON, channel.NotifyWatchlist, channel.NotifyTrending,
		channel.TrendingLimit, releaseTypesJSON, channel.TitleTemplate, channel.BodyTemplate,
		channel.IncludePoster, channel.CreatedAt, channel.UpdatedAt)
	return err
}

func (r *pgNotificationRepo) UpdateChannel(ctx context.Context, channel *models.NotificationChannel) error {
	eventsJSON, _ := json.Marshal(channel.Events)
	releaseTypesJSON, _ := json.Marshal(channel.ReleaseTypes)
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_channels SET
			name=$3, type=$4, url=$5, enabled=$6, events=$7, notify_watchlist=$8,
			notify_trending=$9, trending_limit=$10, release_types=$11, title_template=$12,
			body_template=$13, include_poster=$14, updated_at=$15
		WHERE id=$1 AND profile_id=$2`,
		channel.ID, channel.ProfileID, channel.Name, channel.Type, channel.URL,
		channel.Enabled, eventsJSON, channel.NotifyWatchlist, channel.NotifyTrending,
		channel.TrendingLimit, releaseTypesJSON, channel.TitleTemplate, channel.BodyTemplate,
		channel.IncludePoster, channel.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *pgNotificationRepo) DeleteChannel(ctx context.Context, profileID, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM notification_channels WHERE profile_id=$1 AND id=$2`, profileID, id)
	return err
}

func (r *pgNotificationRepo) GetObservation(ctx context.Context, profileID, itemKey string) (*models.NotificationObservation, error) {
	var observation models.NotificationObservation
	var eventJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT profile_id, item_key, status, event, updated_at
		FROM notification_observations WHERE profile_id=$1 AND item_key=$2`,
		profileID, itemKey).Scan(
		&observation.ProfileID, &observation.ItemKey, &observation.Status, &eventJSON, &observation.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err == nil {
		err = json.Unmarshal(eventJSON, &observation.Event)
	}
	return &observation, err
}

func (r *pgNotificationRepo) ListObservations(ctx context.Context, profileID string) ([]models.NotificationObservation, error) {
	return r.listObservations(ctx, `
		SELECT profile_id, item_key, status, event, updated_at
		FROM notification_observations WHERE profile_id=$1`, profileID)
}

func (r *pgNotificationRepo) ListAllObservations(ctx context.Context) ([]models.NotificationObservation, error) {
	return r.listObservations(ctx, `
		SELECT profile_id, item_key, status, event, updated_at
		FROM notification_observations ORDER BY profile_id, item_key`)
}

func (r *pgNotificationRepo) listObservations(ctx context.Context, query string, args ...any) ([]models.NotificationObservation, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	observations := make([]models.NotificationObservation, 0)
	for rows.Next() {
		var observation models.NotificationObservation
		var eventJSON []byte
		if err := rows.Scan(&observation.ProfileID, &observation.ItemKey, &observation.Status,
			&eventJSON, &observation.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(eventJSON, &observation.Event); err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

func (r *pgNotificationRepo) UpsertObservation(ctx context.Context, observation *models.NotificationObservation) error {
	eventJSON, _ := json.Marshal(observation.Event)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_observations (profile_id, item_key, status, event, updated_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (profile_id, item_key) DO UPDATE SET status=$3, event=$4, updated_at=$5`,
		observation.ProfileID, observation.ItemKey, observation.Status, eventJSON, observation.UpdatedAt)
	return err
}

func scanNotificationProgressMessage(row pgx.Row) (*models.NotificationProgressMessage, error) {
	var message models.NotificationProgressMessage
	err := row.Scan(&message.ChannelID, &message.ProfileID, &message.PlaybackKey,
		&message.MessageID, &message.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &message, err
}

func (r *pgNotificationRepo) GetProgressMessage(ctx context.Context, channelID, playbackKey string) (*models.NotificationProgressMessage, error) {
	return scanNotificationProgressMessage(r.pool.QueryRow(ctx, `
		SELECT channel_id, profile_id, playback_key, message_id, updated_at
		FROM notification_progress_messages
		WHERE channel_id=$1 AND playback_key=$2`, channelID, playbackKey))
}

func (r *pgNotificationRepo) ListProgressMessages(ctx context.Context) ([]models.NotificationProgressMessage, error) {
	return r.listProgressMessages(ctx, `
		SELECT channel_id, profile_id, playback_key, message_id, updated_at
		FROM notification_progress_messages`)
}

func (r *pgNotificationRepo) ListProgressMessagesByPlayback(ctx context.Context, profileID, playbackKey string) ([]models.NotificationProgressMessage, error) {
	return r.listProgressMessages(ctx, `
		SELECT channel_id, profile_id, playback_key, message_id, updated_at
		FROM notification_progress_messages
		WHERE profile_id=$1 AND playback_key=$2`, profileID, playbackKey)
}

func (r *pgNotificationRepo) listProgressMessages(ctx context.Context, query string, args ...any) ([]models.NotificationProgressMessage, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []models.NotificationProgressMessage
	for rows.Next() {
		message, err := scanNotificationProgressMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, *message)
	}
	return messages, rows.Err()
}

func (r *pgNotificationRepo) UpsertProgressMessage(ctx context.Context, message *models.NotificationProgressMessage) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_progress_messages (
			channel_id, profile_id, playback_key, message_id, updated_at
		) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (channel_id, playback_key) DO UPDATE SET
			profile_id=$2, message_id=$4, updated_at=$5`,
		message.ChannelID, message.ProfileID, message.PlaybackKey, message.MessageID, message.UpdatedAt)
	return err
}

func (r *pgNotificationRepo) TouchProgressMessages(ctx context.Context, profileID, playbackKey string, updatedAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_progress_messages SET updated_at=$3
		WHERE profile_id=$1 AND playback_key=$2`, profileID, playbackKey, updatedAt)
	return err
}

func (r *pgNotificationRepo) DeleteProgressMessage(ctx context.Context, channelID, playbackKey string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM notification_progress_messages
		WHERE channel_id=$1 AND playback_key=$2`, channelID, playbackKey)
	return err
}
