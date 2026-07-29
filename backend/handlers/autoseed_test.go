package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/peartube"
)

// autoSeedRelay is a relay that serves a catalog and counts seed submissions,
// which is exactly what an automatic seed exercises: read the catalog, then
// submit only what the swarm is missing.
type autoSeedRelay struct {
	// catalog is the entities array served at GET /api/v1/catalog. An empty
	// string means the relay carries nothing.
	catalog string
	// catalogStatus and catalogBody override the catalog answer, so a gated or
	// broken relay can be simulated.
	catalogStatus int
	catalogBody   string
	// archiveStatus overrides the seed answer, so a refusal can be simulated.
	archiveStatus int
	// archiveDelay holds the seed submission open, so a caller that waits on it
	// is visible as a caller that waits.
	archiveDelay time.Duration

	mu           sync.Mutex
	catalogReads int
	archives     []map[string]any
}

func (relay *autoSeedRelay) archiveCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.archives)
}

func (relay *autoSeedRelay) lastArchive() map[string]any {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.archives) == 0 {
		return nil
	}
	return relay.archives[len(relay.archives)-1]
}

func (relay *autoSeedRelay) client(t *testing.T) *peartube.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/catalog"):
			relay.mu.Lock()
			relay.catalogReads++
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if relay.catalogStatus != 0 {
				w.WriteHeader(relay.catalogStatus)
				_, _ = w.Write([]byte(relay.catalogBody))
				return
			}
			_, _ = w.Write([]byte(`{"entities":[` + relay.catalog + `],"nextCursor":null}`))

		case strings.HasPrefix(r.URL.Path, "/api/v1/archive"):
			if relay.archiveDelay > 0 {
				time.Sleep(relay.archiveDelay)
			}
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			relay.mu.Lock()
			relay.archives = append(relay.archives, body)
			relay.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if relay.archiveStatus != 0 {
				w.WriteHeader(relay.archiveStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"relay exploded","field":null}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"jobId":"arch_1","status":"queued","entityHint":"movie:603"}`))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}
	return client
}

// newAutoSeedHandler builds the handler as main.go wires it, with the switch on
// and a stream resolver that stands in for the composite streaming provider.
func newAutoSeedHandler(t *testing.T, relay *autoSeedRelay, resolver *fakeStreamResolver) *PearTubeHandler {
	t.Helper()
	return &PearTubeHandler{relay: relay.client(t), streams: resolver, autoSeed: true}
}

func moviePlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "tmdb:movie:603",
		MovieName:  "The Matrix",
		Year:       1999,
		SourcePath: "/debrid/torbox/12345/file/9/The.Matrix.1999.mkv",
		Position:   12,
		Duration:   8160,
	}
}

func episodePlayback() models.PlaybackProgressUpdate {
	return models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:1399:s01e02",
		SeriesID:      "tmdb:tv:1399",
		SeriesName:    "Game of Thrones",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		SourcePath:    "/debrid/realdebrid/98765/file/2/GoT.S01E02.mkv",
	}
}

const matrixCatalogEntity = `{"entityId":"tmdb:movie:603","entityKind":"movie","title":"The Matrix","year":1999,` +
	`"sources":[{"publicationId":"pub-matrix","publisherId":"abcdef0123456789","renditionId":"rend-1","byteLength":4096}]}`

// With no relay, or with the switch off, a playback heartbeat must not produce a
// single call — that is what keeps an install which never asked for p2p, or one
// that asked for the relay but not for automatic seeding, behaving as before.
func TestAutoSeedIsInertWhenDisabled(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}

	off := &PearTubeHandler{relay: relay.client(t), streams: resolver, autoSeed: false}
	if _, ok := off.planAutoSeed(moviePlayback()); ok {
		t.Fatal("autoseed planned a seed with the switch off")
	}
	off.OnPlaybackStarted(moviePlayback())

	unconfigured := &PearTubeHandler{autoSeed: true}
	if _, ok := unconfigured.planAutoSeed(moviePlayback()); ok {
		t.Fatal("autoseed planned a seed without a relay")
	}
	unconfigured.OnPlaybackStarted(moviePlayback())

	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved: %q", resolver.asked)
	}
}

// A playback sends a heartbeat every few seconds and a player opens hundreds of
// byte-range requests, none of which reach this handler. Every heartbeat of one
// playback must collapse to a single submission.
func TestAutoSeedSubmitsOncePerTitleAcrossManyHeartbeats(t *testing.T) {
	relay := &autoSeedRelay{}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH-TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	update := moviePlayback()
	for beat := range 50 {
		update.Position = float64(beat * 10)
		plan, ok := handler.planAutoSeed(update)
		if !ok {
			continue
		}
		if plan.key != "movie:603" {
			t.Fatalf("claim key = %q", plan.key)
		}
		// The player's own URL is never forwarded: the seed names the stream
		// path and the seed path re-resolves it.
		if plan.request.SourceURL != "" || plan.request.StreamPath != update.SourcePath {
			t.Fatalf("seed request = %+v", plan.request)
		}
		plan.submit()
	}

	if got := relay.archiveCount(); got != 1 {
		t.Fatalf("archive submissions = %d, want 1", got)
	}
	if resolver.asked != update.SourcePath {
		t.Fatalf("resolver was asked for %q, want %q", resolver.asked, update.SourcePath)
	}
	body := relay.lastArchive()
	if body["url"] != "https://cdn.example.net/d/FRESH-TOKEN/The.Matrix.1999.mkv" {
		t.Fatalf("seeded url = %#v", body["url"])
	}
	if body["contentKind"] != "movie" || body["tmdbId"] != "603" || body["tmdbTitle"] != "The Matrix" {
		t.Fatalf("coordinates = %#v", body)
	}
}

func TestAutoSeedPublishesAnEpisodeUnderItsSeriesCoordinates(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv"})

	plan, ok := handler.planAutoSeed(episodePlayback())
	if !ok {
		t.Fatal("an episode playback was not seedable")
	}
	if plan.key != "show:1399:s1:e2" {
		t.Fatalf("claim key = %q", plan.key)
	}
	plan.submit()

	body := relay.lastArchive()
	if body["contentKind"] != "episode" || body["tmdbId"] != "1399" {
		t.Fatalf("coordinates = %#v", body)
	}
	if body["tmdbSeason"] != float64(1) || body["tmdbEpisode"] != float64(2) {
		t.Fatalf("season/episode = %#v", body)
	}
}

// The relay already serves this title, so asking it to fetch the whole file
// again is pure waste.
func TestAutoSeedSkipsATitleTheSwarmAlreadyServes(t *testing.T) {
	relay := &autoSeedRelay{catalog: matrixCatalogEntity}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the plan was refused before the catalog was consulted")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("archive submissions = %d, want 0", got)
	}
	if relay.catalogReads != 1 {
		t.Fatalf("catalog reads = %d, want 1", relay.catalogReads)
	}
	// Nothing was re-resolved either: a debrid resolve can wake a torrent, and
	// there is no reason to pay for that when the swarm already has the title.
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved anyway: %q", resolver.asked)
	}
}

// Two viewers starting the same title at the same moment must produce one seed.
// Only the in-process claim can enforce that: both would read a catalog that
// does not list the title yet.
func TestAutoSeedFiresOnceForConcurrentPlays(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})

	const plays = 8
	var (
		wait   sync.WaitGroup
		start  = make(chan struct{})
		claims = make(chan autoSeedPlan, plays)
	)
	for range plays {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if plan, ok := handler.planAutoSeed(moviePlayback()); ok {
				claims <- plan
			}
		}()
	}
	close(start)
	wait.Wait()
	close(claims)

	claimed := 0
	for plan := range claims {
		claimed++
		plan.submit()
	}
	if claimed != 1 {
		t.Fatalf("%d of %d concurrent plays claimed the title, want 1", claimed, plays)
	}
	if got := relay.archiveCount(); got != 1 {
		t.Fatalf("archive submissions = %d, want 1", got)
	}
}

// A relay that will not enumerate cannot say whether the swarm already has the
// title. Seeding anyway would mean re-fetching whole files on every catalog
// hiccup, so the attempt is abandoned — but the claim is released, because
// nothing was submitted and a later playback should ask again.
func TestAutoSeedDoesNotSeedWhenTheCatalogIsUnavailable(t *testing.T) {
	relay := &autoSeedRelay{
		catalogStatus: http.StatusForbidden,
		catalogBody:   `{"error":{"code":"OPEN_ACCESS_NOT_ENABLED","message":"not open","field":null}}`,
	}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"}
	handler := newAutoSeedHandler(t, relay, resolver)

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the plan was refused before the catalog was consulted")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 0 {
		t.Fatalf("archive submissions = %d, want 0", got)
	}
	if resolver.asked != "" {
		t.Fatalf("the stream path was resolved anyway: %q", resolver.asked)
	}
	if _, ok := handler.planAutoSeed(moviePlayback()); !ok {
		t.Fatal("the claim was held after nothing was submitted")
	}
}

// A source the relay refuses keeps its claim: it would be refused again on the
// next heartbeat, and one log line per guard window is the right amount of noise.
func TestAutoSeedKeepsItsClaimAfterARefusedSeed(t *testing.T) {
	relay := &autoSeedRelay{archiveStatus: http.StatusInternalServerError}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})

	plan, ok := handler.planAutoSeed(moviePlayback())
	if !ok {
		t.Fatal("the movie was not seedable")
	}
	plan.submit()

	if got := relay.archiveCount(); got != 1 {
		t.Fatalf("archive submissions = %d, want 1", got)
	}
	if _, ok := handler.planAutoSeed(moviePlayback()); ok {
		t.Fatal("a refused seed was retried on the next heartbeat")
	}
}

// Playback that carries no TMDB coordinates, or no source to re-resolve, cannot
// be published and must not be attempted.
func TestAutoSeedSkipsPlaybackItCannotPublish(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/x.mkv"})

	withoutSource := moviePlayback()
	withoutSource.SourcePath = ""

	tvdbOnly := moviePlayback()
	tvdbOnly.ItemID = "tvdb:movie:10702"

	untitled := moviePlayback()
	untitled.MovieName = ""

	partialEpisode := episodePlayback()
	partialEpisode.SeasonNumber = 0

	live := models.PlaybackProgressUpdate{
		MediaType:  "live",
		ItemID:     "live:channel-4",
		SourcePath: "/live/channel-4/stream.ts",
	}

	for name, update := range map[string]models.PlaybackProgressUpdate{
		"no source path":     withoutSource,
		"no tmdb id":         tvdbOnly,
		"no title":           untitled,
		"no season number":   partialEpisode,
		"live tv":            live,
		"no coordinates":     {MediaType: "movie", SourcePath: "/debrid/x/1/file/1"},
		"unknown media type": {MediaType: "photo", ItemID: "tmdb:movie:603", MovieName: "x", SourcePath: "/debrid/x/1/file/1"},
	} {
		if _, ok := handler.planAutoSeed(update); ok {
			t.Fatalf("%s: planned a seed", name)
		}
	}
	if relay.catalogReads != 0 || relay.archiveCount() != 0 {
		t.Fatalf("relay was contacted: %d catalog reads, %d archives", relay.catalogReads, relay.archiveCount())
	}
}

// An episode whose player reported only external ids still publishes: the `tmdb`
// entry of an episode update is its series id, which is the coordinate the swarm
// keys an episode by.
func TestAutoSeedTakesTheTMDBIDFromExternalIDs(t *testing.T) {
	relay := &autoSeedRelay{}
	handler := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv"})

	update := episodePlayback()
	update.ItemID = "tvdb:series:121361:s01e02"
	update.SeriesID = "tvdb:series:121361"
	update.ExternalIDs = map[string]string{"TMDB": "1399", "tvdb": "121361"}

	plan, ok := handler.planAutoSeed(update)
	if !ok {
		t.Fatal("an episode identified by external ids was not seedable")
	}
	if plan.key != "show:1399:s1:e2" {
		t.Fatalf("claim key = %q", plan.key)
	}
}

// autoSeedHistoryService answers the one call the progress endpoint makes. The
// embedded nil interface satisfies the rest: a test that reaches them is asking
// the wrong question and will say so loudly.
type autoSeedHistoryService struct{ historyService }

func (autoSeedHistoryService) UpdatePlaybackProgress(string, models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	return models.PlaybackProgress{ID: "movie:tmdb:movie:603", MediaType: "movie", PercentWatched: 2}, nil
}

// The viewer is the point. A relay that is slow and then fails must not delay,
// alter, or break the heartbeat the player is waiting on.
func TestAutoSeedFailureDoesNotAffectThePlaybackResponse(t *testing.T) {
	relay := &autoSeedRelay{
		archiveStatus: http.StatusInternalServerError,
		archiveDelay:  2 * time.Second,
	}
	seeder := newAutoSeedHandler(t, relay, &fakeStreamResolver{url: "https://cdn.example.net/d/TOKEN/The.Matrix.1999.mkv"})
	handler := &HistoryHandler{Service: autoSeedHistoryService{}, AutoSeeder: seeder}

	body, err := json.Marshal(moviePlayback())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/users/user1/history/progress", strings.NewReader(string(body)))
	req = mux.SetURLVars(req, map[string]string{"userID": "user1", "mediaType": "movie", "id": "tmdb:movie:603"})
	rec := httptest.NewRecorder()

	started := time.Now()
	handler.UpdatePlaybackProgress(rec, req)
	elapsed := time.Since(started)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var progress models.PlaybackProgress
	if err := json.Unmarshal(rec.Body.Bytes(), &progress); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if progress.PercentWatched != 2 {
		t.Fatalf("progress = %+v", progress)
	}
	if elapsed >= relay.archiveDelay {
		t.Fatalf("the heartbeat waited %v on the relay", elapsed)
	}
}
