package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

// Persisted full-reconciliation times. The in-memory lastFullSyncTimes map is
// lost on restart, which used to force a full export (re-sending every local
// play) on the first run after every backend restart.
const (
	lastFullHistoryImportConfigKey = "lastFullHistoryImportAt"
	lastFullHistoryExportConfigKey = "lastFullHistoryExportAt"
)

// Refuse to infer removals when a complete remote listing is missing this many
// previously-seen keys: an empty or truncated provider response must not be
// mirrored locally as a mass unwatch.
const (
	historyRemovalSafetyMinMissing = 50
	historyRemovalSnapshotTimeout  = 30 * time.Second
)

type historySyncSnapshotStore interface {
	Get(ctx context.Context, key string) (*models.HistorySyncSnapshot, error)
	Put(ctx context.Context, snapshot models.HistorySyncSnapshot) error
}

// SetHistorySyncSnapshotStore enables remote-removal detection for
// bidirectional history syncs.
func (s *Service) SetHistorySyncSnapshotStore(store historySyncSnapshotStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historySnapshots = store
}

// lastFullHistorySync returns the most recent full reconciliation recorded in
// memory or persisted in the task config.
func (s *Service) lastFullHistorySync(task config.ScheduledTask, memKey, configKey string) (time.Time, bool) {
	s.lastFullSyncTimesMu.Lock()
	last, ok := s.lastFullSyncTimes[memKey]
	s.lastFullSyncTimesMu.Unlock()
	if persisted, err := time.Parse(time.RFC3339, strings.TrimSpace(task.Config[configKey])); err == nil && (!ok || persisted.After(last)) {
		return persisted, true
	}
	return last, ok
}

// markFullHistorySync records a completed full reconciliation in memory and in
// result.Config, which updateTaskStatus persists only for successful real runs.
func (s *Service) markFullHistorySync(result *SyncResult, memKey, configKey string) {
	s.recordFullHistorySync(memKey)
	if result.Config == nil {
		result.Config = make(map[string]string)
	}
	result.Config[configKey] = time.Now().UTC().Format(time.RFC3339)
}

// historyRemovalTracker detects plays removed on a remote provider since the
// previous bidirectional sync: a key seen on the remote last time that is absent
// from a complete remote listing now. Without it the periodic full export
// re-adds every play the user deletes on the provider.
type historyRemovalTracker struct {
	key       string
	provider  string
	startedAt time.Time
	previous  *models.HistorySyncSnapshot
	prevKeys  map[string]struct{}
	current   map[string]bool // complete remote key set; nil until known
	unsafe    bool
}

// newHistoryRemovalTracker returns nil (no inference, no snapshot writes) for
// one-way tasks or when no snapshot store is configured.
func (s *Service) newHistoryRemovalTracker(task config.ScheduledTask, provider, accountID, profileID string, startedAt time.Time) (*historyRemovalTracker, error) {
	s.mu.RLock()
	store := s.historySnapshots
	s.mu.RUnlock()
	if store == nil || task.Config["syncDirection"] != "bidirectional" {
		return nil, nil
	}
	// Scope by account and profile: switching either must not compare against
	// another account's history and infer mass removals.
	t := &historyRemovalTracker{
		key:       strings.Join([]string{task.ID, provider, accountID, profileID}, ":"),
		provider:  provider,
		startedAt: startedAt,
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyRemovalSnapshotTimeout)
	defer cancel()
	previous, err := store.Get(ctx, t.key)
	if err != nil {
		return nil, fmt.Errorf("load %s history snapshot: %w", provider, err)
	}
	if previous != nil {
		t.previous = previous
		t.prevKeys = make(map[string]struct{}, len(previous.Keys))
		for _, key := range previous.Keys {
			t.prevKeys[key] = struct{}{}
		}
	}
	return t, nil
}

// setCompleteRemote supplies the full current remote key set, enabling removal
// inference for this run when a previous snapshot exists.
func (t *historyRemovalTracker) setCompleteRemote(current map[string]bool) {
	if t == nil {
		return
	}
	t.current = current
	if t.previous == nil {
		return
	}
	missing := 0
	for key := range t.prevKeys {
		if !current[key] {
			missing++
		}
	}
	if len(t.prevKeys) > 0 && (len(current) == 0 || (missing >= historyRemovalSafetyMinMissing && missing*2 > len(t.prevKeys))) {
		t.unsafe = true
		log.Printf("[scheduler] %s history: %d of %d previously seen remote keys are missing (current=%d); not inferring remote removals or replacing the snapshot",
			t.provider, missing, len(t.prevKeys), len(current))
		return
	}
	if missing > 0 {
		log.Printf("[scheduler] %s history: %d previously seen remote keys are gone from the provider", t.provider, missing)
	}
}

// removedRemotely reports whether a watched local item was on the remote at the
// previous sync, is gone now, and has not been re-watched locally since then.
func (t *historyRemovalTracker) removedRemotely(item models.WatchHistoryItem, keys []string) bool {
	if t == nil || t.previous == nil || t.current == nil || t.unsafe || !item.Watched {
		return false
	}
	seenBefore := false
	for _, key := range keys {
		if t.current[key] {
			return false
		}
		if _, ok := t.prevKeys[key]; ok {
			seenBefore = true
		}
	}
	// A newer local watch wins and is exported again.
	return seenBefore && !item.WatchedAt.After(t.previous.SyncedAt)
}

// save records the keys now known to be on the remote. A complete listing
// replaces the snapshot; an incremental one only adds keys to an existing one.
func (s *Service) saveHistoryRemovalSnapshot(t *historyRemovalTracker, present map[string]bool, complete bool) error {
	if t == nil || t.unsafe {
		return nil
	}
	s.mu.RLock()
	store := s.historySnapshots
	s.mu.RUnlock()
	if store == nil {
		return nil
	}
	keys := make([]string, 0, len(present)+len(t.prevKeys))
	changed := t.previous == nil
	if complete {
		for key, ok := range present {
			if ok {
				keys = append(keys, key)
				if _, seen := t.prevKeys[key]; !seen {
					changed = true
				}
			}
		}
		if len(keys) != len(t.prevKeys) {
			changed = true
		}
	} else {
		if t.previous == nil {
			// Incremental listings cannot seed a baseline; wait for a full run.
			return nil
		}
		for key := range t.prevKeys {
			keys = append(keys, key)
		}
		for key, ok := range present {
			if _, seen := t.prevKeys[key]; ok && !seen {
				keys = append(keys, key)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyRemovalSnapshotTimeout)
	defer cancel()
	if err := store.Put(ctx, models.HistorySyncSnapshot{Key: t.key, Keys: keys, SyncedAt: t.startedAt.UTC()}); err != nil {
		return fmt.Errorf("save %s history snapshot: %w", t.provider, err)
	}
	return nil
}

// mirrorRemoteHistoryRemovals marks plays removed on the remote as unwatched
// locally. ImportWatchHistory records the unwatch without live scrobbling, so
// the source provider is not written to again; other providers' scheduled
// exports pick it up as a recent local unwatch.
func (s *Service) mirrorRemoteHistoryRemovals(profileID, provider string, items []models.WatchHistoryItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	s.mu.RLock()
	historySvc := s.historyService
	s.mu.RUnlock()
	unwatched := false
	updates := make([]models.WatchHistoryUpdate, 0, len(items))
	for _, item := range items {
		updates = append(updates, models.WatchHistoryUpdate{
			MediaType: item.MediaType, ItemID: item.ItemID, Name: item.Name, Year: item.Year,
			Watched: &unwatched, ExternalIDs: item.ExternalIDs, SeasonNumber: item.SeasonNumber,
			EpisodeNumber: item.EpisodeNumber, SeriesID: item.SeriesID, SeriesName: item.SeriesName,
		})
	}
	count, err := historySvc.ImportWatchHistory(profileID, updates)
	if err != nil {
		return count, fmt.Errorf("mirror %s history removals: %w", provider, err)
	}
	log.Printf("[scheduler] Marked %d/%d plays removed on %s as unwatched locally", count, len(items), provider)
	return count, nil
}

func historyRemovalDryRunItem(item models.WatchHistoryItem) config.DryRunItem {
	name := item.Name
	if item.MediaType == "episode" && item.SeriesName != "" {
		name = fmt.Sprintf("%s S%02dE%02d", item.SeriesName, item.SeasonNumber, item.EpisodeNumber)
	}
	return config.DryRunItem{Name: name, MediaType: item.MediaType, ID: item.ItemID}
}

// historyAliasKeys are the provider-alias keys used for Trakt/Simkl matching.
func historyAliasKeys(mediaType, itemID string, externalIDs map[string]string) []string {
	ids := alternateItemIDs(mediaType, itemID, externalIDs)
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, strings.ToLower(mediaType)+":"+strings.ToLower(id))
	}
	return keys
}
