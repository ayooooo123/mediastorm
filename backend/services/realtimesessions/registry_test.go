package realtimesessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"novastream/models"
)

type activeStub struct{ activeItems map[string]bool }

func (s activeStub) IsPlaybackActive(_ string, update models.PlaybackProgressUpdate) bool {
	return s.activeItems[update.ItemID]
}

type cleanerStub struct {
	cleaned []string
	err     error
}

type failListOnceStore struct {
	*MemoryStore
	failNext bool
}

func (s *failListOnceStore) List(ctx context.Context) ([]models.RealtimeScrobbleSession, error) {
	if s.failNext {
		s.failNext = false
		return nil, errors.New("temporary list failure")
	}
	return s.MemoryStore.List(ctx)
}

func (s *cleanerStub) CleanupRealtimeSession(_ context.Context, session models.RealtimeScrobbleSession) error {
	s.cleaned = append(s.cleaned, session.ItemID)
	return s.err
}

func ageMemorySession(t *testing.T, store *MemoryStore, provider, userID, mediaType, itemID string, age time.Duration) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	key := sessionRecordKey(provider, userID, mediaType, itemID)
	session, ok := store.sessions[key]
	if !ok {
		t.Fatalf("session %q not found", key)
	}
	session.UpdatedAt = time.Now().Add(-age)
	store.sessions[key] = session
}

func TestSweepCleansOnlySessionsMissingFromDashboard(t *testing.T) {
	store := NewMemoryStore()
	registry := New(store, 0)
	cleaner := &cleanerStub{}
	registry.RegisterCleaner("trakt", cleaner)
	registry.SetActivePlaybackProvider(activeStub{activeItems: map[string]bool{"active": true}})
	registry.Record("trakt", "user", "playing", "", models.PlaybackProgressUpdate{MediaType: "movie", ItemID: "active"}, 25)
	registry.Record("trakt", "user", "paused", "", models.PlaybackProgressUpdate{MediaType: "movie", ItemID: "lingering"}, 40)
	ageMemorySession(t, store, "trakt", "user", "movie", "active", DefaultHeartbeatLease+time.Minute)
	ageMemorySession(t, store, "trakt", "user", "movie", "lingering", DefaultHeartbeatLease+time.Minute)

	registry.Sweep(context.Background())
	if len(cleaner.cleaned) != 1 || cleaner.cleaned[0] != "lingering" {
		t.Fatalf("cleaned = %v, want [lingering]", cleaner.cleaned)
	}
	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ItemID != "active" {
		t.Fatalf("remaining sessions = %+v, want active only", sessions)
	}
}

func TestSweepRetainsRecordWhenProviderCleanupFails(t *testing.T) {
	store := NewMemoryStore()
	registry := New(store, 0)
	registry.RegisterCleaner("scrob", &cleanerStub{err: errors.New("temporary failure")})
	registry.SetActivePlaybackProvider(activeStub{activeItems: map[string]bool{}})
	registry.Record("scrob", "user", "paused", "remote-1", models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "episode"}, 14)
	ageMemorySession(t, store, "scrob", "user", "episode", "episode", DefaultHeartbeatLease+time.Minute)

	registry.Sweep(context.Background())
	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].RemoteKey != "remote-1" {
		t.Fatalf("failed cleanup record was removed: %+v", sessions)
	}
}

func TestSweepRetainsFreshSessionMissingFromDashboard(t *testing.T) {
	store := NewMemoryStore()
	registry := New(store, 0)
	cleaner := &cleanerStub{}
	registry.RegisterCleaner("scrob", cleaner)
	registry.SetActivePlaybackProvider(activeStub{activeItems: map[string]bool{}})
	registry.Record("scrob", "user", "playing", "remote-1", models.PlaybackProgressUpdate{
		MediaType: "episode", ItemID: "episode",
	}, 34)

	registry.Sweep(context.Background())

	if len(cleaner.cleaned) != 0 {
		t.Fatalf("fresh session was cleaned: %v", cleaner.cleaned)
	}
}

func TestTouchRefreshesDurableHeartbeatLease(t *testing.T) {
	store := NewMemoryStore()
	registry := New(store, 0)
	update := models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "episode", Position: 420}
	registry.Record("scrob", "user", "playing", "remote-1", update, 34)
	ageMemorySession(t, store, "scrob", "user", "episode", "episode", DefaultHeartbeatLease+time.Minute)

	key := sessionRecordKey("scrob", "user", "episode", "episode")
	registry.mu.Lock()
	registry.lastPersisted[key] = time.Now().Add(-DefaultHeartbeatPersistPeriod - time.Second)
	registry.mu.Unlock()
	update.Position = 450
	registry.Touch("scrob", "user", "playing", "remote-1", update, 36)

	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].PercentWatched != 36 || sessions[0].Update.Position != 450 {
		t.Fatalf("heartbeat lease was not refreshed: %+v", sessions)
	}
	if time.Since(sessions[0].UpdatedAt) > time.Second {
		t.Fatalf("heartbeat lease timestamp was not refreshed: %s", sessions[0].UpdatedAt)
	}
}

func TestRecoverCleansPersistedSessionEvenWhenPlaybackIsActive(t *testing.T) {
	store := NewMemoryStore()
	registry := New(store, 0)
	cleaner := &cleanerStub{}
	registry.RegisterCleaner("scrob", cleaner)
	registry.SetActivePlaybackProvider(activeStub{activeItems: map[string]bool{"episode": true}})
	registry.Record("scrob", "user", "playing", "remote-1", models.PlaybackProgressUpdate{
		MediaType: "episode", ItemID: "episode",
	}, 39.43)

	registry.Recover(context.Background())

	if len(cleaner.cleaned) != 1 || cleaner.cleaned[0] != "episode" {
		t.Fatalf("cleaned = %v, want [episode]", cleaner.cleaned)
	}
	if !registry.CanStart("scrob", "user", models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "episode"}) {
		t.Fatal("replacement session remained blocked after successful recovery")
	}
	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("persisted sessions = %+v, want none", sessions)
	}
}

func TestFailedRecoveryBlocksOverwriteUntilCleanupSucceeds(t *testing.T) {
	store := NewMemoryStore()
	registry := New(store, 0)
	cleaner := &cleanerStub{err: errors.New("temporary failure")}
	registry.RegisterCleaner("scrob", cleaner)
	update := models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "episode"}
	registry.Record("scrob", "user", "playing", "remote-old", update, 39.43)

	registry.Recover(context.Background())
	if registry.CanStart("scrob", "user", update) {
		t.Fatal("replacement session was allowed while old remote session was unrecovered")
	}
	registry.Record("scrob", "user", "playing", "remote-new", update, 40)
	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].RemoteKey != "remote-old" {
		t.Fatalf("failed recovery record was overwritten: %+v", sessions)
	}

	cleaner.err = nil
	registry.Sweep(context.Background())
	if !registry.CanStart("scrob", "user", update) {
		t.Fatal("replacement session remained blocked after retry succeeded")
	}
}

func TestSweepRetriesRecoveryAfterInitialListFailure(t *testing.T) {
	store := &failListOnceStore{MemoryStore: NewMemoryStore()}
	registry := New(store, 0)
	cleaner := &cleanerStub{}
	registry.RegisterCleaner("scrob", cleaner)
	update := models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "episode"}
	registry.Record("scrob", "user", "playing", "remote-old", update, 39.43)
	store.failNext = true

	registry.Recover(context.Background())
	if registry.CanStart("scrob", "other-user", models.PlaybackProgressUpdate{MediaType: "movie", ItemID: "movie"}) {
		t.Fatal("new sessions were allowed before persisted recovery could be listed")
	}

	registry.Sweep(context.Background())
	if len(cleaner.cleaned) != 1 || cleaner.cleaned[0] != "episode" {
		t.Fatalf("cleaned = %v, want recovery retry to clean [episode]", cleaner.cleaned)
	}
	if !registry.CanStart("scrob", "user", update) {
		t.Fatal("sessions remained blocked after recovery list retry succeeded")
	}
}
