package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"novastream/models"
)

type memoryRepo struct {
	mu           sync.Mutex
	channels     map[string]models.NotificationChannel
	observations map[string]models.NotificationObservation
}

func newMemoryRepo() *memoryRepo {
	return &memoryRepo{
		channels:     make(map[string]models.NotificationChannel),
		observations: make(map[string]models.NotificationObservation),
	}
}

func (r *memoryRepo) GetChannel(_ context.Context, id string) (*models.NotificationChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel, ok := r.channels[id]
	if !ok {
		return nil, nil
	}
	return &channel, nil
}

func (r *memoryRepo) ListChannels(_ context.Context, profileID string) ([]models.NotificationChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var channels []models.NotificationChannel
	for _, channel := range r.channels {
		if channel.ProfileID == profileID {
			channels = append(channels, channel)
		}
	}
	return channels, nil
}

func (r *memoryRepo) CreateChannel(_ context.Context, channel *models.NotificationChannel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[channel.ID] = *channel
	return nil
}

func (r *memoryRepo) UpdateChannel(_ context.Context, channel *models.NotificationChannel) error {
	return r.CreateChannel(context.Background(), channel)
}

func (r *memoryRepo) DeleteChannel(_ context.Context, profileID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if channel, ok := r.channels[id]; ok && channel.ProfileID == profileID {
		delete(r.channels, id)
	}
	return nil
}

func observationID(profileID, itemKey string) string { return profileID + "\x00" + itemKey }

func (r *memoryRepo) GetObservation(_ context.Context, profileID, itemKey string) (*models.NotificationObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	observation, ok := r.observations[observationID(profileID, itemKey)]
	if !ok {
		return nil, nil
	}
	return &observation, nil
}

func (r *memoryRepo) ListObservations(_ context.Context, profileID string) ([]models.NotificationObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var observations []models.NotificationObservation
	for _, observation := range r.observations {
		if observation.ProfileID == profileID {
			observations = append(observations, observation)
		}
	}
	return observations, nil
}

func (r *memoryRepo) UpsertObservation(_ context.Context, observation *models.NotificationObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations[observationID(observation.ProfileID, observation.ItemKey)] = *observation
	return nil
}

func TestFormatRendersSupportedSections(t *testing.T) {
	title, body := Format(models.NotificationChannel{
		TitleTemplate: "{{eventLabel}}: {{title}}",
		BodyTemplate:  "{{mediaLabel}}{{progressLabel}}{{releaseLabel}}",
	}, models.NotificationEvent{
		Type:          models.NotificationEventWatchResumed,
		Title:         "Pilot",
		MediaType:     "episode",
		SeriesTitle:   "Example Show",
		EpisodeTitle:  "Pilot",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		Percent:       42,
	})
	if title != "Resumed: Example Show" {
		t.Fatalf("title = %q", title)
	}
	if body != "Episode · S01E02 · Pilot · 42%" {
		t.Fatalf("body = %q", body)
	}
}

func TestSaveAndListChannelPreservesAndMasksWebhookURL(t *testing.T) {
	repo := newMemoryRepo()
	service := New(repo)
	defer service.Close()

	saved, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile", Name: "Automation", Type: models.NotificationChannelWebhook,
		URL: "https://example.com/hook/secret", Enabled: true,
		Events: []string{models.NotificationEventWatchWatched},
	})
	if err != nil {
		t.Fatalf("save channel: %v", err)
	}
	if saved.URL != "" || !saved.URLConfigured {
		t.Fatalf("saved URL exposure = %q configured=%t", saved.URL, saved.URLConfigured)
	}

	saved.Name = "Renamed"
	saved.URL = ""
	if _, err := service.SaveChannel(context.Background(), saved); err != nil {
		t.Fatalf("update channel: %v", err)
	}
	stored := repo.channels[saved.ID]
	if stored.URL != "https://example.com/hook/secret" {
		t.Fatalf("stored URL = %q", stored.URL)
	}

	listed, err := service.ListChannels(context.Background(), "profile")
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(listed) != 1 || listed[0].URL != "" || !listed[0].URLConfigured {
		t.Fatalf("listed channel = %#v", listed)
	}
}

func TestReleaseDeliveryDeduplicatesOverlappingSources(t *testing.T) {
	received := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, Events: []string{models.NotificationEventRelease},
		NotifyWatchlist: true, NotifyTrending: true, TrendingLimit: 20,
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	base := models.NotificationEvent{
		ID: "watchlist", Type: models.NotificationEventRelease, ProfileID: "profile",
		Title: "Movie", MediaType: "movie", ReleaseType: "digital", ReleaseDate: "2026-07-23",
		ExternalIDs: map[string]string{"tvdb": "2"}, Source: "watchlist", OccurredAt: time.Now().UTC(),
	}
	service.Notify(base)
	base.ID = "trending"
	base.Source = "top-trending"
	base.SourceRank = 1
	base.ExternalIDs = map[string]string{"tmdb": "1", "tvdb": "2", "imdb": "tt0000001"}
	service.Notify(base)
	base.ID = "trending-tmdb-only"
	base.ExternalIDs = map[string]string{"tmdb": "1"}
	service.Notify(base)

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for release event")
	}
	select {
	case duplicate := <-received:
		t.Fatalf("received duplicate event %q", duplicate)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSaveChannelRequiresSingleNotificationType(t *testing.T) {
	repo := newMemoryRepo()
	service := New(repo)
	defer service.Close()

	_, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile",
		Name:      "Mixed",
		Type:      models.NotificationChannelWebhook,
		URL:       "https://example.com/hook",
		Events: []string{
			models.NotificationEventWatchStarted,
			models.NotificationEventRelease,
		},
		NotifyWatchlist: true,
	})
	if err == nil {
		t.Fatal("expected mixed notification types to be rejected")
	}
}

func TestReleaseIdentityUsesCanonicalExternalID(t *testing.T) {
	watchlist := models.NotificationEvent{
		Title:       "Movie",
		Year:        2026,
		MediaType:   "movie",
		ReleaseType: "digital",
		ReleaseDate: "2026-07-23",
		ExternalIDs: map[string]string{"tmdb": "1"},
	}
	trending := watchlist
	trending.ExternalIDs = map[string]string{
		"TMDB_ID": "1",
		"imdb":    "tt0000001",
	}
	if got, want := releaseEventIdentity(trending), releaseEventIdentity(watchlist); got != want {
		t.Fatalf("release identities differ: got %q, want %q", got, want)
	}
}

func TestPlaybackNotificationsAreEdgeTriggered(t *testing.T) {
	received := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true,
		Events: []string{
			models.NotificationEventWatchStarted,
			models.NotificationEventWatchPlaying,
			models.NotificationEventWatchResumed,
			models.NotificationEventWatchWatched,
		},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:1", MovieName: "Movie", Duration: 100,
	}
	service.HandlePlaybackUpdate("profile", update, 1)
	update.IsPaused = true
	service.HandlePlaybackUpdate("profile", update, 10)
	update.IsPaused = false
	service.HandlePlaybackUpdate("profile", update, 11)
	service.HandlePlaybackUpdate("profile", update, 95)

	want := []string{
		models.NotificationEventWatchStarted,
		models.NotificationEventWatchPlaying,
		models.NotificationEventWatchResumed,
		models.NotificationEventWatchWatched,
	}
	for i, expected := range want {
		select {
		case actual := <-received:
			if actual != expected {
				t.Fatalf("event %d = %q, want %q", i, actual, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %q", expected)
		}
	}
}

func TestObserveCalendarReleasesDurableDueObservation(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, Events: []string{models.NotificationEventRelease},
		NotifyWatchlist: true, TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	repo.observations[observationID("profile", "due")] = models.NotificationObservation{
		ProfileID: "profile", ItemKey: "due", Status: "upcoming",
		Event: models.NotificationEvent{
			ID: "release", Type: models.NotificationEventRelease, ProfileID: "profile",
			Title: "Due Movie", MediaType: "movie", ReleaseDate: time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
			Source: "watchlist", OccurredAt: time.Now().UTC(),
		},
	}
	service := New(repo)
	defer service.Close()
	service.ObserveCalendar("profile", nil)

	select {
	case actual := <-received:
		if actual != models.NotificationEventRelease {
			t.Fatalf("event = %q", actual)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for release event")
	}

	observation := repo.observations[observationID("profile", "due")]
	if observation.Status != "released" {
		t.Fatalf("status = %q", observation.Status)
	}
}
