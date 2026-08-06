package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/notifications"
)

// newAutoSeedTracker builds a stream tracker wired the way main wires the global
// one: the notification observer first, then the p2p integration on both the
// matched-activity fanout and the playback-start signal.
func newAutoSeedTracker(seeder *PearTubeHandler, observers ...PlaybackActivityObserver) *StreamTracker {
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	for _, observer := range observers {
		tracker.AddPlaybackActivityObserver(observer)
	}
	tracker.AddPlaybackActivityObserver(seeder)
	tracker.SetPlaybackAutoSeeder(seeder)
	return tracker
}

// appPlaybackRequest is the byte-range request an app makes: a stream path plus
// the media identity the player was launched with. This is the only playback
// signal an app produces — it never calls the progress endpoint.
func appPlaybackRequest(rangeStart int64) *http.Request {
	request := httptest.NewRequest(http.MethodGet,
		"/video/stream?profileId=profile&mediaType=movie&itemId=tmdb:movie:603"+
			"&movieName=The+Matrix&year=1999", nil)
	request.Header.Set("Range", "bytes="+strconv.FormatInt(rangeStart, 10)+"-")
	return request
}

const appStreamPath = "/debrid/torbox/55944852/file/0/The.Matrix.1999.mkv"

// waitForArchives waits for the relay to receive want submissions and then
// confirms no further one arrives, so "exactly once" is asserted rather than
// "at least once".
func waitForArchives(t *testing.T, relay *autoSeedRelay, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for relay.archiveCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("archive submissions = %d, want %d", relay.archiveCount(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	if got := relay.archiveCount(); got != want {
		t.Fatalf("archive submissions = %d, want %d", got, want)
	}
}

// One playback from an app is hundreds of byte-range requests, each of which
// opens and closes its own tracked stream, plus whatever heartbeats the player
// sends. Every one of them is a playback-start signal, and all of them together
// must produce a single seed.
func TestAutoSeedFiresOncePerPlaybackAcrossEveryPlaybackSignal(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv"}
	seeder := newAutoSeedHandler(t, relay, resolver)
	tracker := newAutoSeedTracker(seeder)

	// The app's transport: a fresh range request every few seconds, each ending
	// before the next begins, so every single one looks like a new playback to
	// the tracker and only the seeder's claim can collapse them.
	for chunk := range 200 {
		id, _, _ := tracker.StartStreamWithAccount(
			appPlaybackRequest(int64(chunk)*4194304), appStreamPath, 4194304, int64(chunk)*4194304, 0, "acct1")
		tracker.EndStream(id)
	}

	// The same playback also reaches the matched-activity fanout, which is what
	// the web player's heartbeat and the HLS keepalive feed.
	update := moviePlayback()
	update.SourcePath = appStreamPath
	for beat := range 50 {
		update.Position = float64(beat * 10)
		seeder.HandlePlaybackUpdate("profile", update, float64(beat))
	}

	waitForArchives(t, relay, 1)
	if relay.catalogReads != 1 {
		t.Fatalf("catalog reads = %d, want 1", relay.catalogReads)
	}
	body := relay.lastArchive()
	if body["contentKind"] != "movie" || body["tmdbId"] != "603" || body["tmdbTitle"] != "The Matrix" {
		t.Fatalf("coordinates = %#v", body)
	}
	// The relay is handed a freshly re-resolved address, never the reference the
	// player was given: a Torbox stream path is not a URL at all.
	if resolver.asked != appStreamPath {
		t.Fatalf("resolver was asked for %q, want %q", resolver.asked, appStreamPath)
	}
	if body["url"] != resolver.url {
		t.Fatalf("seeded url = %#v", body["url"])
	}
}

// An episode played from an app has to reach the swarm under its series' TMDB
// coordinates, which the stream request carries as seriesId — and, when the
// player's own ids came from another provider, as the tmdb external id.
func TestAppEpisodePlaybackSeedsUnderItsSeriesCoordinates(t *testing.T) {
	relay := &autoSeedRelay{}
	const streamPath = "/debrid/realdebrid/98765/file/2/GoT.S01E02.mkv"
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/GoT.S01E02.mkv"}
	seeder := newAutoSeedHandler(t, relay, resolver)
	tracker := newAutoSeedTracker(seeder)

	request := httptest.NewRequest(http.MethodGet,
		"/video/stream?profileId=profile&mediaType=episode&itemId=tvdb:series:121361:s01e02"+
			"&seriesId=tvdb:series:121361&seriesName=Game+of+Thrones"+
			"&seasonNumber=1&episodeNumber=2&tmdb=1399", nil)
	tracker.StartStreamWithAccount(request, streamPath, 4194304, 0, 0, "acct1")

	waitForArchives(t, relay, 1)
	body := relay.lastArchive()
	if body["contentKind"] != "episode" || body["tmdbId"] != "1399" {
		t.Fatalf("coordinates = %#v", body)
	}
	if body["tmdbTitle"] != "Game of Thrones" {
		t.Fatalf("title = %#v", body["tmdbTitle"])
	}
	if body["tmdbSeason"] != float64(1) || body["tmdbEpisode"] != float64(2) {
		t.Fatalf("season/episode = %#v / %#v", body["tmdbSeason"], body["tmdbEpisode"])
	}
	if resolver.asked != streamPath {
		t.Fatalf("resolver was asked for %q, want %q", resolver.asked, streamPath)
	}
}

// The web player behind a transcode reports progress to the HLS keepalive rather
// than to the tracker, and that payload used to name no source at all. Both
// consumers must see it, and what they see must be seedable.
func TestHLSKeepAliveFeedsEveryObserverASeedableUpdate(t *testing.T) {
	watcher := &capturePlaybackActivityObserver{calls: make(chan models.PlaybackProgressUpdate, 2)}
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH/The.Matrix.1999.mkv"}
	seeder := newAutoSeedHandler(t, relay, resolver)

	manager := &HLSManager{
		sessions: map[string]*HLSSession{
			"session-1": {
				ID:                 "session-1",
				ProfileID:          "profile",
				Path:               appStreamPath,
				Duration:           8160,
				LastSegmentRequest: time.Now(),
				MediaMetadata: StreamMediaMetadata{
					MediaType: "movie",
					ItemID:    "tmdb:movie:603",
					MovieName: "The Matrix",
					Year:      1999,
				},
			},
		},
	}
	manager.AddPlaybackActivityObserver(watcher)
	manager.AddPlaybackActivityObserver(seeder)

	for beat := 1; beat <= 5; beat++ {
		request := httptest.NewRequest(http.MethodPost,
			"/keepalive?time="+strconv.Itoa(beat*10), nil)
		response := httptest.NewRecorder()
		manager.KeepAlive(response, request, "session-1")
		if response.Code != http.StatusOK {
			t.Fatalf("KeepAlive() status = %d", response.Code)
		}
	}

	select {
	case update := <-watcher.calls:
		if update.PlaybackSessionID != "hls:session-1" {
			t.Fatalf("PlaybackSessionID = %q", update.PlaybackSessionID)
		}
		if update.SourcePath != appStreamPath {
			t.Fatalf("SourcePath = %q, want %q", update.SourcePath, appStreamPath)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first observer lost its callback to the fanout")
	}
	waitForArchives(t, relay, 1)
}

// A playback the tracker is already following must not re-announce itself on
// every overlapping range connection. This is the cheap half of the dedupe: it
// keeps the seeder from being called at all, whereas the claim keeps it from
// submitting twice.
func TestPlaybackStartIsAnnouncedOncePerConcurrentPlaybackSlot(t *testing.T) {
	starts := make(chan models.PlaybackProgressUpdate, 8)
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	tracker.SetPlaybackAutoSeeder(capturePlaybackStarts{starts})

	for chunk := range 5 {
		// Overlapping connections: nothing is ended, exactly as a native player
		// keeping several ranges open at once.
		tracker.StartStreamWithAccount(
			appPlaybackRequest(int64(chunk)*4194304), appStreamPath, 4194304, int64(chunk)*4194304, 0, "acct1")
	}

	var first models.PlaybackProgressUpdate
	select {
	case first = <-starts:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the playback start")
	}
	select {
	case extra := <-starts:
		t.Fatalf("a second range connection re-announced the playback: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	// The payload has to carry both halves of what a seed needs: the TMDB
	// coordinates the app launched with, and a stream path that can be resolved
	// again server-side.
	if first.MediaType != "movie" || first.ItemID != "tmdb:movie:603" {
		t.Fatalf("coordinates = %+v", first)
	}
	if first.MovieName != "The Matrix" || first.Year != 1999 {
		t.Fatalf("title = %q (%d)", first.MovieName, first.Year)
	}
	if first.SourcePath != appStreamPath {
		t.Fatalf("SourcePath = %q, want %q", first.SourcePath, appStreamPath)
	}
	if first.PlaybackSessionID != "direct:profile|movie:tmdb:movie:603" {
		t.Fatalf("PlaybackSessionID = %q", first.PlaybackSessionID)
	}
}

// A stream nothing identifies cannot be published, and a live channel has no
// TMDB coordinates at all. Neither may reach the seeder.
func TestPlaybackStartIsNotAnnouncedWithoutMediaIdentity(t *testing.T) {
	starts := make(chan models.PlaybackProgressUpdate, 4)
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	tracker.SetPlaybackAutoSeeder(capturePlaybackStarts{starts})

	probe := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=profile", nil)
	tracker.StartStreamWithAccount(probe, "/webdav/nzbs/unknown.mkv", 1000, 0, 0, "acct1")

	live := httptest.NewRequest(http.MethodGet, "/video/live?profileId=profile&mediaType=live&itemId=channel:1", nil)
	tracker.StartStreamWithAccount(live, "http://iptv.example/live.ts", 0, 0, 0, "acct1")

	select {
	case update := <-starts:
		t.Fatalf("an unpublishable playback reached the seeder: %+v", update)
	case <-time.After(100 * time.Millisecond):
	}
}

type capturePlaybackStarts struct {
	starts chan models.PlaybackProgressUpdate
}

func (c capturePlaybackStarts) OnPlaybackStarted(update models.PlaybackProgressUpdate) {
	c.starts <- update
}

// The notification service was the only consumer of matched playback activity.
// Adding the p2p integration must not cost it a single callback.
func TestPlaybackObserverFanoutKeepsNotificationsAndSeeding(t *testing.T) {
	events := make(chan string, 8)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		events <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	notifier := notifications.New(watchStartedRepo{url: webhook.URL})
	defer notifier.Close()

	relay := &autoSeedRelay{}
	seeder := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	tracker := newAutoSeedTracker(seeder, notifier)

	tracker.mu.Lock()
	tracker.streams["range-1"] = &TrackedStream{
		ID:           "range-1",
		ProfileID:    "profile",
		Path:         appStreamPath,
		LastActivity: time.Now(),
		MediaMetadata: StreamMediaMetadata{
			MediaType: "movie",
			ItemID:    "tmdb:movie:603",
			MovieName: "The Matrix",
			Year:      1999,
		},
	}
	tracker.mu.Unlock()

	// A heartbeat carrying no source path of its own: the matched stream names
	// one, which is what makes this payload seedable.
	matched := tracker.ObservePlaybackActivity("profile", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:movie:603",
		Duration:  8160,
		Position:  12,
	}, 1)
	if matched != 1 {
		t.Fatalf("ObservePlaybackActivity() matched %d streams, want 1", matched)
	}

	select {
	case event := <-events:
		if event != models.NotificationEventWatchStarted {
			t.Fatalf("notification event = %q, want %q", event, models.NotificationEventWatchStarted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notification service lost its callback to the fanout")
	}
	waitForArchives(t, relay, 1)
}

// The viewer and the notification service are the point; the swarm is a bonus. A
// relay that hangs and then refuses must cost neither of them anything.
func TestSeedFailureDisturbsNeitherPlaybackNorNotifications(t *testing.T) {
	events := make(chan string, 8)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		events <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	notifier := notifications.New(watchStartedRepo{url: webhook.URL})
	defer notifier.Close()

	relay := &autoSeedRelay{
		archiveStatus: http.StatusInternalServerError,
		archiveDelay:  2 * time.Second,
	}
	seeder := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	tracker := newAutoSeedTracker(seeder, notifier)

	started := time.Now()
	id, _, _ := tracker.StartStreamWithAccount(appPlaybackRequest(0), appStreamPath, 4194304, 0, 0, "acct1")
	if id == "" {
		t.Fatal("the stream was not tracked")
	}
	matched := tracker.ObservePlaybackActivity("profile", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:movie:603",
		Duration:  8160,
		Position:  12,
	}, 1)
	elapsed := time.Since(started)

	if matched != 1 {
		t.Fatalf("ObservePlaybackActivity() matched %d streams, want 1", matched)
	}
	if elapsed >= relay.archiveDelay {
		t.Fatalf("playback waited %v on the relay", elapsed)
	}
	select {
	case event := <-events:
		if event != models.NotificationEventWatchStarted {
			t.Fatalf("notification event = %q, want %q", event, models.NotificationEventWatchStarted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failing seed cost the notification service its callback")
	}

	// The refused submission stays refused rather than being retried on every
	// range request, and the failure never leaves the seeder.
	waitForArchives(t, relay, 1)
}

// watchStartedRepo is the smallest notification store that produces a watch
// event: one enabled webhook channel, and nothing persisted.
type watchStartedRepo struct{ url string }

func (r watchStartedRepo) ListChannels(context.Context, string) ([]models.NotificationChannel, error) {
	return []models.NotificationChannel{{
		ID:        "channel",
		ProfileID: "profile",
		Type:      models.NotificationChannelWebhook,
		URL:       r.url,
		Enabled:   true,
		Events:    []string{models.NotificationEventWatchStarted},
	}}, nil
}

// The repository grew a profile-agnostic listing; this fixture owns exactly one
// channel, so both listings return it.
func (r watchStartedRepo) ListAllChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	return r.ListChannels(ctx, "profile")
}

func (watchStartedRepo) GetChannel(context.Context, string) (*models.NotificationChannel, error) {
	return nil, nil
}
func (watchStartedRepo) CreateChannel(context.Context, *models.NotificationChannel) error { return nil }
func (watchStartedRepo) UpdateChannel(context.Context, *models.NotificationChannel) error { return nil }
func (watchStartedRepo) DeleteChannel(context.Context, string, string) error              { return nil }
func (watchStartedRepo) GetObservation(context.Context, string, string) (*models.NotificationObservation, error) {
	return nil, nil
}
func (watchStartedRepo) ListObservations(context.Context, string) ([]models.NotificationObservation, error) {
	return nil, nil
}
func (watchStartedRepo) ListAllObservations(context.Context) ([]models.NotificationObservation, error) {
	return nil, nil
}
func (watchStartedRepo) UpsertObservation(context.Context, *models.NotificationObservation) error {
	return nil
}
func (watchStartedRepo) GetProgressMessage(context.Context, string, string) (*models.NotificationProgressMessage, error) {
	return nil, nil
}
func (watchStartedRepo) ListProgressMessages(context.Context) ([]models.NotificationProgressMessage, error) {
	return nil, nil
}
func (watchStartedRepo) ListProgressMessagesByPlayback(context.Context, string, string) ([]models.NotificationProgressMessage, error) {
	return nil, nil
}
func (watchStartedRepo) UpsertProgressMessage(context.Context, *models.NotificationProgressMessage) error {
	return nil
}
func (watchStartedRepo) TouchProgressMessages(context.Context, string, string, time.Time) error {
	return nil
}
func (watchStartedRepo) DeleteProgressMessage(context.Context, string, string) error { return nil }

var _ datastore.NotificationRepository = watchStartedRepo{}
