package datastore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"

	"novastream/models"
)

type pgUserCalendarRepo struct{ pool DB }

// UserCalendars persists each profile's last built calendar so restarts can
// serve it before the background refresh finishes.
func (ds *DataStore) UserCalendars() UserCalendarRepository {
	return &pgUserCalendarRepo{pool: ds.pool}
}

func (r *pgUserCalendarRepo) List(ctx context.Context) ([]models.UserCalendarSnapshot, error) {
	rows, err := r.pool.Query(ctx, `SELECT user_id, kind, body, refreshed_at, stale FROM user_calendars`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.UserCalendarSnapshot
	for rows.Next() {
		var snap models.UserCalendarSnapshot
		var body []byte
		if err := rows.Scan(&snap.UserID, &snap.Kind, &body, &snap.RefreshedAt, &snap.Stale); err != nil {
			return nil, err
		}
		items, err := decodeUserCalendarItems(body)
		if err != nil {
			// A corrupt row only costs that profile a rebuild.
			continue
		}
		snap.Items = items
		out = append(out, snap)
	}
	return out, rows.Err()
}

func (r *pgUserCalendarRepo) Put(ctx context.Context, snap models.UserCalendarSnapshot) error {
	body, err := encodeUserCalendarItems(snap.Items)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO user_calendars (user_id, kind, body, item_count, refreshed_at, stale) VALUES ($1,$2,$3,$4,$5,$6)
 ON CONFLICT (user_id, kind) DO UPDATE SET body=EXCLUDED.body, item_count=EXCLUDED.item_count, refreshed_at=EXCLUDED.refreshed_at, stale=EXCLUDED.stale`,
		snap.UserID, snap.Kind, body, len(snap.Items), snap.RefreshedAt, snap.Stale)
	return err
}

func (r *pgUserCalendarRepo) MarkStale(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE user_calendars SET stale=TRUE WHERE user_id=$1`, userID)
	return err
}

func encodeUserCalendarItems(items []models.CalendarItem) ([]byte, error) {
	if items == nil {
		items = []models.CalendarItem{}
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(items); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeUserCalendarItems(body []byte) ([]models.CalendarItem, error) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	items := []models.CalendarItem{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}
