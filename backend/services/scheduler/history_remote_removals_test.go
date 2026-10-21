package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/scrob"
	"novastream/services/simkl"
	"novastream/services/trakt"
)

type memoryHistorySnapshotStore struct {
	mu        sync.Mutex
	snapshots map[string]models.HistorySyncSnapshot
	puts      int
}

func newMemoryHistorySnapshotStore() *memoryHistorySnapshotStore {
	return &memoryHistorySnapshotStore{snapshots: make(map[string]models.HistorySyncSnapshot)}
}

func (m *memoryHistorySnapshotStore) Get(_ context.Context, key string) (*models.HistorySyncSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot, ok := m.snapshots[key]
	if !ok {
		return nil, nil
	}
	snapshot.Keys = append([]string(nil), snapshot.Keys...)
	return &snapshot, nil
}

func (m *memoryHistorySnapshotStore) Put(_ context.Context, snapshot models.HistorySyncSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.snapshots[snapshot.Key] = snapshot
	return nil
}

// backdate makes every local watch after this point count as a re-watch.
func (m *memoryHistorySnapshotStore) backdate(at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, snapshot := range m.snapshots {
		snapshot.SyncedAt = at
		m.snapshots[key] = snapshot
	}
}

func containsID(ids []int, want int) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func localWatched(t *testing.T, h *history.Service, tmdbID int) bool {
	t.Helper()
	items, err := h.ListWatchHistory("profile")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.MediaType == "movie" && item.ExternalIDs["tmdb"] == fmt.Sprint(tmdbID) {
			return item.Watched
		}
	}
	t.Fatalf("movie %d missing from local history", tmdbID)
	return false
}

func rewatchLocally(t *testing.T, h *history.Service, tmdbID int, at time.Time) {
	t.Helper()
	watched := true
	if _, err := h.ImportWatchHistory("profile", []models.WatchHistoryUpdate{{
		MediaType: "movie", ItemID: fmt.Sprintf("tmdb:movie:%d", tmdbID), Watched: &watched, WatchedAt: at,
		ExternalIDs: map[string]string{"tmdb": fmt.Sprint(tmdbID)},
	}}); err != nil {
		t.Fatal(err)
	}
}

// remoteHistoryHarness serves a mutable remote movie history for one provider
// and records the TMDB IDs the export adds.
type remoteHistoryHarness struct {
	t      *testing.T
	mu     sync.Mutex
	remote []int
	added  []int
}

func (h *remoteHistoryHarness) setRemote(ids ...int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remote = ids
	h.added = nil
}

func (h *remoteHistoryHarness) addedIDs() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.added...)
}

func (h *remoteHistoryHarness) recordAdded(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload struct {
		Movies []struct {
			IDs struct {
				TMDB int `json:"tmdb"`
			} `json:"ids"`
		} `json:"movies"`
		TMDBID int `json:"tmdb_id"` // Scrob adds one event per request
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		h.t.Fatalf("decode export: %v (%s)", err, body)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, movie := range payload.Movies {
		h.added = append(h.added, movie.IDs.TMDB)
	}
	if payload.TMDBID > 0 {
		h.added = append(h.added, payload.TMDBID)
	}
}

func (h *remoteHistoryHarness) roundTrip(provider string) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		h.mu.Lock()
		remote := append([]int(nil), h.remote...)
		h.mu.Unlock()
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/auth/login") {
			return jsonResponse(200, `{"access_token":"token"}`), nil
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			h.t.Fatalf("%s: unexpected %s %s", provider, r.Method, r.URL.Path)
		}
		if r.Method == http.MethodPost {
			if strings.HasSuffix(r.URL.Path, "/remove") {
				h.t.Fatalf("%s: unexpected remote removal %s", provider, r.URL.Path)
			}
			h.recordAdded(r)
			return jsonResponse(201, `{}`), nil
		}
		var rows []string
		switch provider {
		case "trakt":
			for _, id := range remote {
				rows = append(rows, fmt.Sprintf(`{"type":"movie","watched_at":"2000-01-01T00:00:00Z","movie":{"ids":{"tmdb":%d}}}`, id))
			}
			return jsonResponse(200, "["+strings.Join(rows, ",")+"]"), nil
		case "simkl":
			if r.URL.Path != "/sync/all-items/movies" {
				return jsonResponse(200, `{}`), nil
			}
			for _, id := range remote {
				rows = append(rows, fmt.Sprintf(`{"status":"completed","movie":{"ids":{"tmdb":%d}},"last_watched_at":"2000-01-01T00:00:00Z"}`, id))
			}
			return jsonResponse(200, `{"movies":[`+strings.Join(rows, ",")+`]}`), nil
		case "mdblist":
			for _, id := range remote {
				rows = append(rows, fmt.Sprintf(`{"movie":{"ids":{"tmdb":%d}},"last_watched_at":"2000-01-01T00:00:00Z"}`, id))
			}
			return jsonResponse(200, `{"movies":[`+strings.Join(rows, ",")+`],"episodes":[]}`), nil
		case "scrob":
			for i, id := range remote {
				rows = append(rows, fmt.Sprintf(`{"completed":true,"media":{"id":%d,"type":"movie","tmdb_id":%d}}`, i+1, id))
			}
			return jsonResponse(200, `{"total_pages":1,"results":[`+strings.Join(rows, ",")+`]}`), nil
		}
		h.t.Fatalf("unknown provider %s", provider)
		return nil, nil
	}
}

func (h *remoteHistoryHarness) export(svc *Service, provider string, task config.ScheduledTask, dryRun bool) (SyncResult, error) {
	switch provider {
	case "trakt":
		return svc.syncLocalHistoryToTrakt(task, &config.TraktAccount{ID: "acct"}, "profile", dryRun)
	case "simkl":
		return svc.syncLocalHistoryToSimkl(task, &config.SimklAccount{ID: "acct", ClientID: "client", AccessToken: "token"}, "profile", dryRun)
	case "mdblist":
		return svc.syncLocalHistoryToMDBList(task, &config.MDBListAccount{ID: "acct", APIKey: "test"}, "profile", dryRun)
	case "scrob":
		return svc.syncLocalHistoryToScrob(task, &config.ScrobAccount{ID: "acct", BaseURL: "https://scrob.example", APIKey: "test", Username: "user", Password: "pass"}, "profile", dryRun)
	}
	h.t.Fatalf("unknown provider %s", provider)
	return SyncResult{}, nil
}

func newRemoteHistoryHarness(t *testing.T, provider string, h *history.Service, store *memoryHistorySnapshotStore) (*remoteHistoryHarness, *Service) {
	t.Helper()
	harness := &remoteHistoryHarness{t: t}
	transport := harness.roundTrip(provider)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = transport
	svc := &Service{historyService: h, lastFullSyncTimes: map[string]time.Time{}}
	svc.SetHistorySyncSnapshotStore(store)
	switch provider {
	case "trakt":
		svc.traktClient = trakt.NewClient("client", "secret")
	case "simkl":
		svc.simklClient = simkl.NewClient()
		svc.simklClient.SetHTTPClientForTest(&http.Client{Transport: transport})
	case "scrob":
		svc.scrobClient = scrob.NewClientWithHTTPClient(&http.Client{Transport: transport})
	}
	return harness, svc
}

func TestBidirectionalHistoryExportHonoursRemoteRemovals(t *testing.T) {
	for _, provider := range []string{"trakt", "simkl", "mdblist", "scrob"} {
		t.Run(provider, func(t *testing.T) {
			h := auditHistoryService(t, 3, true) // movies 1-3 watched locally in 2000
			store := newMemoryHistorySnapshotStore()
			harness, svc := newRemoteHistoryHarness(t, provider, h, store)
			// No LastRunAt: every run below is a full export.
			task := config.ScheduledTask{ID: "task", Config: map[string]string{"syncDirection": "bidirectional"}}

			// First run: no snapshot, so nothing is inferred and the play missing
			// on the remote is exported as before.
			harness.setRemote(1, 2)
			result, err := harness.export(svc, provider, task, false)
			if err != nil || len(result.ToRemove) != 0 {
				t.Fatalf("first run result=%+v err=%v", result, err)
			}
			if added := harness.addedIDs(); !containsID(added, 3) {
				t.Fatalf("first run exported %v, want 3 included", added)
			}
			if len(store.snapshots) != 1 || !localWatched(t, h, 2) {
				t.Fatalf("first run snapshots=%d", len(store.snapshots))
			}

			// Movie 2 is removed on the remote: preview lists it without changes.
			harness.setRemote(1, 3)
			puts := store.puts
			preview, err := harness.export(svc, provider, task, true)
			if err != nil || len(preview.ToRemove) != 1 || preview.ToRemove[0].ID != "tmdb:movie:2" {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			for _, item := range preview.ToAdd {
				if item.ID == "tmdb:movie:2" {
					t.Fatalf("preview re-exports the removed play: %+v", preview)
				}
			}
			if store.puts != puts || !localWatched(t, h, 2) || len(harness.addedIDs()) != 0 {
				t.Fatal("preview mutated local history, the snapshot or the remote")
			}

			// Real run: not re-exported, mirrored locally as an unwatch.
			if _, err := harness.export(svc, provider, task, false); err != nil {
				t.Fatal(err)
			}
			if added := harness.addedIDs(); containsID(added, 2) {
				t.Fatalf("removed play re-exported: %v", added)
			}
			if localWatched(t, h, 2) {
				t.Fatal("remote removal was not mirrored locally")
			}
			if !localWatched(t, h, 1) || !localWatched(t, h, 3) {
				t.Fatal("unrelated plays were unwatched")
			}
		})
	}
}

func TestBidirectionalHistoryExportLocalRewatchWinsOverRemoteRemoval(t *testing.T) {
	for _, provider := range []string{"trakt", "simkl", "mdblist", "scrob"} {
		t.Run(provider, func(t *testing.T) {
			h := auditHistoryService(t, 2, true)
			store := newMemoryHistorySnapshotStore()
			harness, svc := newRemoteHistoryHarness(t, provider, h, store)
			task := config.ScheduledTask{ID: "task", Config: map[string]string{"syncDirection": "bidirectional"}}

			harness.setRemote(1, 2)
			if _, err := harness.export(svc, provider, task, false); err != nil {
				t.Fatal(err)
			}
			// Movie 2 is removed remotely, then watched again locally after the
			// last sync: the newer local watch is exported.
			store.backdate(time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC))
			rewatchLocally(t, h, 2, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
			harness.setRemote(1)
			if _, err := harness.export(svc, provider, task, false); err != nil {
				t.Fatal(err)
			}
			if added := harness.addedIDs(); !containsID(added, 2) {
				t.Fatalf("re-watch exported %v, want 2 included", added)
			}
			if !localWatched(t, h, 2) {
				t.Fatal("local re-watch was unwatched")
			}
		})
	}
}

func TestOneWayHistoryExportStillReexportsRemoteRemovals(t *testing.T) {
	h := auditHistoryService(t, 2, true)
	store := newMemoryHistorySnapshotStore()
	harness, svc := newRemoteHistoryHarness(t, "trakt", h, store)
	task := config.ScheduledTask{ID: "task", Config: map[string]string{"syncDirection": "local_to_trakt"}}
	harness.setRemote(1)
	if _, err := harness.export(svc, "trakt", task, false); err != nil {
		t.Fatal(err)
	}
	if added := harness.addedIDs(); !containsID(added, 2) || store.puts != 0 {
		t.Fatalf("added=%v puts=%d", added, store.puts)
	}
}

func TestHistoryRemovalTrackerRefusesMassRemoval(t *testing.T) {
	previous := &models.HistorySyncSnapshot{SyncedAt: time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)}
	prevKeys := make(map[string]struct{})
	current := make(map[string]bool)
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("movie:tmdb:movie:%d", i)
		previous.Keys = append(previous.Keys, key)
		prevKeys[key] = struct{}{}
		if i < 20 {
			current[key] = true
		}
	}
	item := models.WatchHistoryItem{MediaType: "movie", ItemID: "tmdb:movie:150", Watched: true, WatchedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
	keys := historyAliasKeys(item.MediaType, item.ItemID, nil)
	for _, remote := range []map[string]bool{{}, current} {
		tracker := &historyRemovalTracker{provider: "test", previous: previous, prevKeys: prevKeys}
		tracker.setCompleteRemote(remote)
		if tracker.removedRemotely(item, keys) {
			t.Fatalf("inferred removal from a listing with %d of %d keys", len(remote), len(prevKeys))
		}
		store := newMemoryHistorySnapshotStore()
		svc := &Service{historySnapshots: store}
		if err := svc.saveHistoryRemovalSnapshot(tracker, remote, true); err != nil || store.puts != 0 {
			t.Fatalf("suspect listing replaced the snapshot (puts=%d err=%v)", store.puts, err)
		}
	}
}

func TestHistoryFullExportTimerSurvivesRestart(t *testing.T) {
	h := auditHistoryService(t, 1, true)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	var startAt []string
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			return jsonResponse(201, `{}`), nil
		}
		if r.URL.Path != "/users/me/history" {
			return jsonResponse(200, `[]`), nil
		}
		startAt = append(startAt, r.URL.Query().Get("start_at"))
		return jsonResponse(200, `[{"type":"movie","watched_at":"2000-01-01T00:00:00Z","movie":{"ids":{"tmdb":1}}}]`), nil
	})
	task := config.ScheduledTask{ID: "task", Config: map[string]string{}}
	result, err := (&Service{historyService: h, traktClient: trakt.NewClient("client", "secret")}).syncHistoryBidirectional(task, &config.TraktAccount{}, "profile", false)
	if err != nil || result.Config[lastFullHistoryImportConfigKey] == "" || result.Config[lastFullHistoryExportConfigKey] == "" {
		t.Fatalf("full run did not record its completion: result=%+v err=%v", result, err)
	}

	// updateTaskStatus persists the result config into the saved task.
	manager := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.ScheduledTasks.Tasks = []config.ScheduledTask{task}
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	(&Service{configManager: manager}).updateTaskStatus("task", nil, result)
	saved, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	persisted := saved.ScheduledTasks.Tasks[0]
	if persisted.Config[lastFullHistoryExportConfigKey] != result.Config[lastFullHistoryExportConfigKey] {
		t.Fatalf("timer not persisted: %+v", persisted.Config)
	}

	// A fresh scheduler (empty in-memory timers, as after a restart) stays incremental.
	startAt = nil
	restarted := &Service{historyService: h, traktClient: trakt.NewClient("client", "secret"), lastFullSyncTimes: map[string]time.Time{}}
	if _, err := restarted.syncHistoryBidirectional(persisted, &config.TraktAccount{}, "profile", false); err != nil {
		t.Fatal(err)
	}
	if len(startAt) == 0 {
		t.Fatal("no Trakt requests after restart")
	}
	for _, cursor := range startAt {
		if cursor == "" {
			t.Fatalf("restart triggered a full Trakt history pass: %v", startAt)
		}
	}
}
