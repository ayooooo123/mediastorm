package calendar

import (
	"context"
	"sync"
	"testing"
	"time"

	"novastream/models"
)

type fakeStore struct {
	mu    sync.Mutex
	rows  map[string]models.UserCalendarSnapshot
	stale []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]models.UserCalendarSnapshot{}}
}

func (f *fakeStore) List(_ context.Context) ([]models.UserCalendarSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.UserCalendarSnapshot, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeStore) Put(_ context.Context, snap models.UserCalendarSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[snap.UserID+"|"+snap.Kind] = snap
	return nil
}

func (f *fakeStore) MarkStale(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stale = append(f.stale, userID)
	return nil
}

func (f *fakeStore) get(key string) (models.UserCalendarSnapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[key]
	return row, ok
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestBuildPersistsFullAndLite(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.trending["movie"] = []models.TrendingItem{{Title: models.Title{
		Name: "Film", TMDBID: 7, MediaType: "movie",
		Releases: []models.Release{{Type: "digital", Date: futureDate(5)}},
	}}}
	store := newFakeStore()
	svc := New(meta, wl, hist, us, users)
	svc.SetStore(store)

	svc.buildAndCacheUserCalendar("user1", true)
	svc.buildAndCacheLiteUserCalendar("user1", true, nil)

	waitFor(t, "full snapshot", func() bool { _, ok := store.get("user1|full"); return ok })
	waitFor(t, "lite snapshot", func() bool { _, ok := store.get("user1|lite"); return ok })
	full, _ := store.get("user1|full")
	if len(full.Items) != 1 || full.RefreshedAt.IsZero() || full.Stale {
		t.Fatalf("unexpected full snapshot: %+v", full)
	}
}

func TestLoadPersistedSeedsCaches(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	store := newFakeStore()
	now := time.Now().UTC()
	item := models.CalendarItem{Title: "Show", AirDate: futureDate(1)}
	store.rows["a|full"] = models.UserCalendarSnapshot{UserID: "a", Kind: kindFull, Items: []models.CalendarItem{item}, RefreshedAt: now.Add(-time.Hour)}
	store.rows["a|lite"] = models.UserCalendarSnapshot{UserID: "a", Kind: kindLite, Items: []models.CalendarItem{item}, RefreshedAt: now.Add(-time.Hour)}
	store.rows["b|full"] = models.UserCalendarSnapshot{UserID: "b", Kind: kindFull, Items: []models.CalendarItem{item}, RefreshedAt: now, Stale: true}
	store.rows["c|full"] = models.UserCalendarSnapshot{UserID: "c", Kind: kindFull, Items: []models.CalendarItem{item}, RefreshedAt: now.Add(-30 * 24 * time.Hour)}
	store.rows["d|full"] = models.UserCalendarSnapshot{UserID: "d", Kind: kindFull, RefreshedAt: now.Add(-time.Hour)}

	svc := New(meta, wl, hist, us, users)
	existing := &userCalendar{RefreshedAt: now}
	svc.cache["d"] = existing
	svc.SetStore(store)

	if loaded := svc.LoadPersisted(context.Background()); loaded != 2 {
		t.Fatalf("expected 2 loaded entries, got %d", loaded)
	}
	if svc.peek("a") == nil || svc.peekLite("a") == nil {
		t.Fatal("expected fresh full and lite entries for a")
	}
	if svc.peek("b") != nil || svc.stale["b"] == nil {
		t.Fatal("stale snapshot must only be a home fallback")
	}
	if svc.peek("c") != nil || svc.stale["c"] == nil {
		t.Fatal("very old snapshot must only be a home fallback")
	}
	if svc.peek("d") != existing {
		t.Fatal("persisted entry must not replace an in-memory build")
	}
}

func TestRefreshAllSkipsFreshProfiles(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	users.users = []models.User{{ID: "fresh"}, {ID: "old"}, {ID: "missing"}}
	svc := New(meta, wl, hist, us, users)
	svc.cache["fresh"] = &userCalendar{RefreshedAt: time.Now().Add(-time.Hour)}
	svc.cache["old"] = &userCalendar{RefreshedAt: time.Now().Add(-5 * time.Hour)}

	svc.refreshAll(nil, 4*time.Hour)

	if wl.listCalls["fresh"] != 0 {
		t.Fatalf("fresh profile was rebuilt (%d watchlist reads)", wl.listCalls["fresh"])
	}
	if wl.listCalls["old"] == 0 || wl.listCalls["missing"] == 0 {
		t.Fatalf("old/missing profiles must be rebuilt: %v", wl.listCalls)
	}

	// Without a threshold (periodic/manual refresh) everyone is rebuilt.
	svc.refreshAll(nil, 0)
	if wl.listCalls["fresh"] == 0 {
		t.Fatal("forced refresh must rebuild fresh profiles too")
	}
}

func TestRefreshAllLiteSkipsFreshProfiles(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	users.users = []models.User{{ID: "fresh"}, {ID: "missing"}}
	hist.items["fresh"] = []models.SeriesWatchState{{SeriesTitle: "X", ExternalIDs: map[string]string{"tvdb": "1"}}}
	hist.items["missing"] = []models.SeriesWatchState{{SeriesTitle: "Y", ExternalIDs: map[string]string{"tvdb": "2"}}}
	svc := New(meta, wl, hist, us, users)
	svc.liteCache["fresh"] = &userCalendar{RefreshedAt: time.Now()}

	svc.refreshAllLite(nil, time.Hour)

	if wl.listCalls["fresh"] != 0 || wl.listCalls["missing"] == 0 {
		t.Fatalf("unexpected lite rebuilds: %v", wl.listCalls)
	}
}

func TestCycleMemoSharesTrendingAcrossProfiles(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	users.users = []models.User{{ID: "u1"}, {ID: "u2"}, {ID: "u3"}}
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{Name: "Hot", TVDBID: 100, Status: "Continuing"},
		Seasons: []models.SeriesSeason{{Number: 1, Episodes: []models.SeriesEpisode{
			{Name: "Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(3)},
		}}},
	}
	meta.trending["series"] = []models.TrendingItem{{Title: models.Title{Name: "Hot", TVDBID: 100, Status: "Continuing", MediaType: "series"}}}
	svc := New(meta, wl, hist, us, users)

	svc.refreshAll(newCycleMetadata(meta), 0)

	if meta.trendingCalls["series"] != 1 || meta.trendingCalls["movie"] != 1 {
		t.Fatalf("expected one trending fetch per media type per cycle, got %v", meta.trendingCalls)
	}
	if meta.seriesLiteCalls != 1 {
		t.Fatalf("expected one series details fetch per cycle, got %d", meta.seriesLiteCalls)
	}
	for _, u := range users.users {
		cal := svc.peek(u.ID)
		if cal == nil || len(cal.Items) != 1 {
			t.Fatalf("profile %s should still get its own item, got %+v", u.ID, cal)
		}
	}

	// A new cycle fetches again.
	svc.refreshAll(newCycleMetadata(meta), 0)
	if meta.trendingCalls["series"] != 2 {
		t.Fatalf("expected a fresh fetch in a new cycle, got %v", meta.trendingCalls)
	}
}

func TestGetForHomeNeverBlocks(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	wl.delays["user1"] = 300 * time.Millisecond
	svc := New(meta, wl, hist, us, users)

	start := time.Now()
	uc, pending := svc.GetForHome("user1")
	if uc != nil || !pending {
		t.Fatalf("expected pending with no calendar, got %+v pending=%t", uc, pending)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("GetForHome blocked on a build")
	}
	waitFor(t, "kicked full build", func() bool { return svc.peek("user1") != nil })
	uc, pending = svc.GetForHome("user1")
	if uc == nil || pending {
		t.Fatal("expected the built calendar once the background build finished")
	}
}

func TestGetForHomeFallbacks(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	wl.delays["user1"] = 200 * time.Millisecond
	store := newFakeStore()
	svc := New(meta, wl, hist, us, users)
	svc.SetStore(store)

	lite := &userCalendar{RefreshedAt: time.Now()}
	svc.liteCache["user1"] = lite
	if uc, pending := svc.GetForHome("user1"); uc != lite || pending {
		t.Fatal("expected lite calendar when full is missing")
	}

	full := &userCalendar{RefreshedAt: time.Now()}
	waitFor(t, "kicked full build", func() bool { return svc.peek("user1") != nil })
	svc.mu.Lock()
	svc.cache["user1"] = full
	svc.mu.Unlock()
	if uc, _ := svc.GetForHome("user1"); uc != full {
		t.Fatal("expected full calendar to win")
	}

	svc.Invalidate("user1")
	if uc, pending := svc.GetForHome("user1"); uc != full || pending {
		t.Fatal("expected the pre-invalidation calendar as a stale fallback")
	}
	waitFor(t, "persisted stale mark", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.stale) == 1
	})
	waitFor(t, "rebuild clears stale", func() bool {
		svc.mu.RLock()
		defer svc.mu.RUnlock()
		return svc.cache["user1"] != nil && svc.stale["user1"] == nil
	})
}
