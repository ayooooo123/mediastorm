package history

import (
	"testing"
	"time"

	"novastream/models"
)

func TestPlanWatchHistoryPersistenceOnlyReturnsChangesAndDeletes(t *testing.T) {
	unchanged := models.WatchHistoryItem{
		ID: "movie:1", MediaType: "movie", ItemID: "1", Watched: true,
		UpdatedAt: time.Unix(100, 0), ExternalIDs: map[string]string{"tmdb": "1"},
	}
	changedBefore := models.WatchHistoryItem{
		ID: "movie:2", MediaType: "movie", ItemID: "2", Watched: false,
		UpdatedAt: time.Unix(100, 0),
	}
	deleted := models.WatchHistoryItem{
		ID: "movie:3", MediaType: "movie", ItemID: "3", Watched: true,
		UpdatedAt: time.Unix(100, 0),
	}
	persistedItems := map[string]map[string]models.WatchHistoryItem{
		"user": {
			unchanged.ID:     unchanged,
			changedBefore.ID: changedBefore,
			deleted.ID:       deleted,
		},
	}
	persisted, err := watchHistoryPersistenceFingerprints(persistedItems)
	if err != nil {
		t.Fatalf("watchHistoryPersistenceFingerprints returned error: %v", err)
	}

	changedAfter := changedBefore
	changedAfter.Watched = true
	changedAfter.UpdatedAt = time.Unix(200, 0)
	added := models.WatchHistoryItem{
		ID: "movie:4", MediaType: "movie", ItemID: "4", Watched: true,
		UpdatedAt: time.Unix(200, 0),
	}
	current := map[string]map[string]models.WatchHistoryItem{
		"user": {
			unchanged.ID:    unchanged,
			changedAfter.ID: changedAfter,
			added.ID:        added,
		},
	}

	upserts, deletes, err := planWatchHistoryPersistence(current, persisted)
	if err != nil {
		t.Fatalf("planWatchHistoryPersistence returned error: %v", err)
	}
	if len(upserts) != 2 {
		t.Fatalf("upserts = %d, want 2", len(upserts))
	}
	gotUpserts := map[string]bool{}
	for _, change := range upserts {
		gotUpserts[change.key.itemID] = true
	}
	if !gotUpserts[changedAfter.ID] || !gotUpserts[added.ID] || gotUpserts[unchanged.ID] {
		t.Fatalf("unexpected upsert IDs: %v", gotUpserts)
	}
	if len(deletes) != 1 || deletes[0].itemID != deleted.ID {
		t.Fatalf("deletes = %#v, want %q", deletes, deleted.ID)
	}
}

func TestPlanPlaybackProgressPersistenceIgnoresRuntimeOnlyChanges(t *testing.T) {
	item := models.PlaybackProgress{
		ID: "movie:1", MediaType: "movie", ItemID: "1",
		Position: 30, Duration: 100, UpdatedAt: time.Unix(100, 0),
		ExternalIDs: map[string]string{"tmdb": "1"},
	}
	persistedItems := map[string]map[string]models.PlaybackProgress{
		"user": {item.ID: item},
	}
	persisted, err := playbackProgressPersistenceFingerprints(persistedItems)
	if err != nil {
		t.Fatalf("playbackProgressPersistenceFingerprints returned error: %v", err)
	}

	runtimeChanged := item
	runtimeChanged.IsBuffering = true
	allowed := true
	runtimeChanged.AllowedToContinue = &allowed
	runtimeChanged.MigrationRequested = true
	runtimeChanged.MigrationReason = "runtime only"
	current := map[string]map[string]models.PlaybackProgress{
		"user": {runtimeChanged.ID: runtimeChanged},
	}

	upserts, deletes, err := planPlaybackProgressPersistence(current, persisted)
	if err != nil {
		t.Fatalf("planPlaybackProgressPersistence returned error: %v", err)
	}
	if len(upserts) != 0 || len(deletes) != 0 {
		t.Fatalf("runtime-only changes produced upserts=%d deletes=%d", len(upserts), len(deletes))
	}
}

func TestPlanPlaybackProgressPersistenceReturnsChangedAndDeletedRows(t *testing.T) {
	unchanged := models.PlaybackProgress{
		ID: "movie:1", MediaType: "movie", ItemID: "1",
		Position: 10, Duration: 100, UpdatedAt: time.Unix(100, 0),
	}
	changed := models.PlaybackProgress{
		ID: "movie:2", MediaType: "movie", ItemID: "2",
		Position: 20, Duration: 100, UpdatedAt: time.Unix(100, 0),
	}
	deleted := models.PlaybackProgress{
		ID: "movie:3", MediaType: "movie", ItemID: "3",
		Position: 30, Duration: 100, UpdatedAt: time.Unix(100, 0),
	}
	persistedItems := map[string]map[string]models.PlaybackProgress{
		"user": {
			unchanged.ID: unchanged,
			changed.ID:   changed,
			deleted.ID:   deleted,
		},
	}
	persisted, err := playbackProgressPersistenceFingerprints(persistedItems)
	if err != nil {
		t.Fatalf("playbackProgressPersistenceFingerprints returned error: %v", err)
	}

	changed.Position = 40
	changed.UpdatedAt = time.Unix(200, 0)
	current := map[string]map[string]models.PlaybackProgress{
		"user": {
			unchanged.ID: unchanged,
			changed.ID:   changed,
		},
	}

	upserts, deletes, err := planPlaybackProgressPersistence(current, persisted)
	if err != nil {
		t.Fatalf("planPlaybackProgressPersistence returned error: %v", err)
	}
	if len(upserts) != 1 || upserts[0].key.itemID != changed.ID {
		t.Fatalf("upserts = %#v, want only %q", upserts, changed.ID)
	}
	if len(deletes) != 1 || deletes[0].itemID != deleted.ID {
		t.Fatalf("deletes = %#v, want only %q", deletes, deleted.ID)
	}
}
