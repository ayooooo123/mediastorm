package datastore

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgHistorySyncSnapshotRepo struct{ pool DB }

// HistorySyncSnapshots stores remote key sets used to detect plays removed on
// a provider between bidirectional history syncs.
func (ds *DataStore) HistorySyncSnapshots() HistorySyncSnapshotRepository {
	return &pgHistorySyncSnapshotRepo{pool: ds.pool}
}

func (r *pgHistorySyncSnapshotRepo) Get(ctx context.Context, key string) (*models.HistorySyncSnapshot, error) {
	snapshot := &models.HistorySyncSnapshot{Key: key}
	var body []byte
	err := r.pool.QueryRow(ctx, `SELECT body, synced_at FROM history_sync_snapshots WHERE snapshot_key=$1`, key).Scan(&body, &snapshot.SyncedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys, err := decodeHistorySyncSnapshotKeys(body)
	if err != nil {
		return nil, err
	}
	snapshot.Keys = keys
	return snapshot, nil
}

func (r *pgHistorySyncSnapshotRepo) Put(ctx context.Context, snapshot models.HistorySyncSnapshot) error {
	body, err := encodeHistorySyncSnapshotKeys(snapshot.Keys)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO history_sync_snapshots (snapshot_key, body, key_count, synced_at) VALUES ($1,$2,$3,$4)
 ON CONFLICT (snapshot_key) DO UPDATE SET body=EXCLUDED.body, key_count=EXCLUDED.key_count, synced_at=EXCLUDED.synced_at`,
		snapshot.Key, body, len(snapshot.Keys), snapshot.SyncedAt)
	return err
}

// Keys are short and highly repetitive (provider:id:sNNeNN), so a gzip'd
// newline-joined list keeps large libraries to a few tens of KB.
func encodeHistorySyncSnapshotKeys(keys []string) ([]byte, error) {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := io.WriteString(zw, strings.Join(sorted, "\n")); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeHistorySyncSnapshotKeys(body []byte) ([]string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []string{}, nil
	}
	return strings.Split(string(raw), "\n"), nil
}
