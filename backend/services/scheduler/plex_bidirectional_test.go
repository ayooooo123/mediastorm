package scheduler

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/plex"
	"novastream/services/watchlist"
)

const plexBidirectionalTestSource = "plex:acc:task"

// plexBidirectionalStub serves a fixed Plex watchlist and records watchlist
// add/remove actions.
type plexBidirectionalStub struct {
	mu      sync.Mutex
	added   []string
	removed []string
}

// The Plex client captures http.DefaultTransport when created, so the stub
// assigns svc a fresh client after swapping the transport.
func installPlexBidirectionalStub(t *testing.T, svc *Service, watchlistJSON string) *plexBidirectionalStub {
	t.Helper()
	stub := &plexBidirectionalStub{}
	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "discover.provider.plex.tv" {
			t.Fatalf("unexpected request: %s", req.URL)
			return nil, io.EOF
		}
		switch req.URL.Path {
		case "/library/sections/watchlist/all":
			return jsonResponse(http.StatusOK, watchlistJSON), nil
		case "/library/metadata/plex-dune":
			return jsonResponse(http.StatusOK, `{"MediaContainer":{"Metadata":[{"guid":"plex://movie/plex-dune","Guid":[{"id":"tmdb://438631"}]}]}}`), nil
		case "/library/metadata/plex-arrival":
			return jsonResponse(http.StatusOK, `{"MediaContainer":{"Metadata":[{"guid":"plex://movie/plex-arrival","Guid":[{"id":"tmdb://329865"}]}]}}`), nil
		case "/actions/addToWatchlist":
			stub.mu.Lock()
			stub.added = append(stub.added, req.URL.Query().Get("ratingKey"))
			stub.mu.Unlock()
			return jsonResponse(http.StatusOK, `{}`), nil
		case "/actions/removeFromWatchlist":
			stub.mu.Lock()
			stub.removed = append(stub.removed, req.URL.Query().Get("ratingKey"))
			stub.mu.Unlock()
			return jsonResponse(http.StatusOK, `{}`), nil
		default:
			t.Fatalf("unexpected Plex request: %s", req.URL)
			return nil, io.EOF
		}
	})
	t.Cleanup(func() { http.DefaultTransport = origTransport })
	svc.plexClient = plex.NewClient("test-client")
	return stub
}

const emptyPlexWatchlistJSON = `{"MediaContainer":{"size":0,"totalSize":0,"offset":0,"Metadata":[]}}`

const dunePlexWatchlistJSON = `{
	"MediaContainer": {
		"size": 1,
		"totalSize": 1,
		"Metadata": [{
			"ratingKey": "plex-dune",
			"guid": "plex://movie/plex-dune",
			"type": "movie",
			"title": "Dune",
			"year": 2021
		}]
	}
}`

func newPlexBidirectionalTestService(t *testing.T) (*Service, *watchlist.Service) {
	t.Helper()
	watchlistSvc, err := watchlist.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("watchlist.NewService() error = %v", err)
	}
	return &Service{watchlistService: watchlistSvc}, watchlistSvc
}

func seedDune(t *testing.T, watchlistSvc *watchlist.Service, syncSource string) {
	t.Helper()
	if _, err := watchlistSvc.AddOrUpdate("profile-1", models.WatchlistUpsert{
		ID:          "tmdb:movie:438631",
		MediaType:   "movie",
		Name:        "Dune",
		Year:        2021,
		ExternalIDs: map[string]string{"tmdb": "438631", "plex": "plex-dune"},
		SyncSource:  syncSource,
	}); err != nil {
		t.Fatalf("seed Dune: %v", err)
	}
}

func TestSyncBidirectional_MirrorRemovesLocalItemRemovedFromPlex(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, plexBidirectionalTestSource)
	stub := installPlexBidirectionalStub(t, svc, emptyPlexWatchlistJSON)

	since := time.Now().Add(-time.Hour)
	if _, err := svc.syncBidirectional("plex-token", "profile-1", plexBidirectionalTestSource, "mirror", "source_wins", &since, false); err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}

	if len(stub.added) != 0 {
		t.Fatalf("re-added to Plex: %v", stub.added)
	}
	items, _ := watchlistSvc.List("profile-1")
	if len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune removed", items)
	}
	// A sync removal is not a user removal: re-adding on Plex must import again.
	if watchlistSvc.RemovedSince("profile-1", "movie", "tmdb:movie:438631", nil, time.Time{}) {
		t.Fatal("sync removal left a user-removal tombstone")
	}
}

func TestSyncBidirectional_MirrorExportsUnsyncedLocalItem(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, "")
	stub := installPlexBidirectionalStub(t, svc, emptyPlexWatchlistJSON)

	since := time.Now().Add(-time.Hour)
	result, err := svc.syncBidirectional("plex-token", "profile-1", plexBidirectionalTestSource, "mirror", "source_wins", &since, false)
	if err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}

	if len(stub.added) != 1 || stub.added[0] != "plex-dune" {
		t.Fatalf("added = %v, want [plex-dune]", stub.added)
	}
	items, _ := watchlistSvc.List("profile-1")
	if len(items) != 1 || items[0].SyncSource != plexBidirectionalTestSource {
		t.Fatalf("local items = %#v, want Dune marked as synced", items)
	}
	if result.Config[watchlistSyncCursorKey] == "" {
		t.Fatal("missing deletion cursor in result config")
	}
}

func TestSyncBidirectional_AdditiveKeepsAndReexportsSyncedItem(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, plexBidirectionalTestSource)
	stub := installPlexBidirectionalStub(t, svc, emptyPlexWatchlistJSON)

	since := time.Now().Add(-time.Hour)
	if _, err := svc.syncBidirectional("plex-token", "profile-1", plexBidirectionalTestSource, "additive", "source_wins", &since, false); err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}

	if len(stub.added) != 1 {
		t.Fatalf("added = %v, want Dune re-exported in additive mode", stub.added)
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 1 {
		t.Fatalf("local items = %#v, want Dune kept", items)
	}
}

func TestSyncBidirectional_MirrorRemovesFromPlexAfterLocalRemoval(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, plexBidirectionalTestSource)
	since := time.Now().Add(-time.Minute)
	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	stub := installPlexBidirectionalStub(t, svc, dunePlexWatchlistJSON)

	if _, err := svc.syncBidirectional("plex-token", "profile-1", plexBidirectionalTestSource, "mirror", "source_wins", &since, false); err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}

	if len(stub.removed) != 1 || stub.removed[0] != "plex-dune" {
		t.Fatalf("removed = %v, want [plex-dune]", stub.removed)
	}
	if items, _ := watchlistSvc.List("profile-1"); len(items) != 0 {
		t.Fatalf("local items = %#v, want Dune not re-imported", items)
	}
}

func TestSyncBidirectional_IgnoresLocalRemovalBeforeLastSync(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, plexBidirectionalTestSource)
	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	stub := installPlexBidirectionalStub(t, svc, dunePlexWatchlistJSON)

	// The removal was already propagated by an earlier run; Dune being back on
	// Plex means it was re-added there.
	since := time.Now().Add(time.Second)
	if _, err := svc.syncBidirectional("plex-token", "profile-1", plexBidirectionalTestSource, "mirror", "source_wins", &since, false); err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}
	if len(stub.removed) != 0 {
		t.Fatalf("removed = %v, want none", stub.removed)
	}

	// Before the first run there is nothing to compare against.
	if _, err := svc.syncBidirectional("plex-token", "profile-1", plexBidirectionalTestSource, "mirror", "source_wins", nil, false); err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}
	if len(stub.removed) != 0 {
		t.Fatalf("removed = %v, want none on first run", stub.removed)
	}
}

func TestWatchlistSyncSince(t *testing.T) {
	lastRun := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cursor := time.Date(2026, 10, 2, 3, 4, 5, 0, time.UTC)

	if got := watchlistSyncSince(config.ScheduledTask{}); got != nil {
		t.Fatalf("since = %v, want nil before first run", got)
	}
	if got := watchlistSyncSince(config.ScheduledTask{LastRunAt: &lastRun}); got == nil || !got.Equal(lastRun) {
		t.Fatalf("since = %v, want last run fallback", got)
	}
	task := config.ScheduledTask{
		LastRunAt: &lastRun,
		Config:    map[string]string{watchlistSyncCursorKey: cursor.Format(time.RFC3339Nano)},
	}
	if got := watchlistSyncSince(task); got == nil || !got.Equal(cursor) {
		t.Fatalf("since = %v, want cursor", got)
	}
}

func TestSyncBidirectional_DryRunOmitsLocallyRemovedItems(t *testing.T) {
	svc, watchlistSvc := newPlexBidirectionalTestService(t)
	seedDune(t, watchlistSvc, plexBidirectionalTestSource)
	if _, err := watchlistSvc.Remove("profile-1", "movie", "tmdb:movie:438631"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	installPlexBidirectionalStub(t, svc, dunePlexWatchlistJSON)

	// The removal predates since, so it is not pushed to Plex, and the import
	// skips it; the dry run must not claim Dune would be imported.
	since := time.Now().Add(time.Second)
	result, err := svc.syncBidirectional("plex-token", "profile-1", plexBidirectionalTestSource, "mirror", "source_wins", &since, true)
	if err != nil {
		t.Fatalf("syncBidirectional() error = %v", err)
	}
	if len(result.ToAdd) != 0 || len(result.ToRemove) != 0 {
		t.Fatalf("dry run ToAdd = %#v, ToRemove = %#v, want neither", result.ToAdd, result.ToRemove)
	}
}
