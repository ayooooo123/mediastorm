package peartube

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
)

// The switch is on by default: an operator who configured a relay wants the
// swarm to grow. Only an explicit false value turns the automatic trigger off.
func TestAutoSeedIsOnUnlessExplicitlyDisabled(t *testing.T) {
	autoSeedFromEnv := func(value string) bool {
		return resolve(config.PearTubeSettings{}, func(key string) string {
			if key == AutoSeedEnv {
				return value
			}
			return ""
		}).AutoSeed
	}
	for _, value := range []string{"", "1", "true", "yes", "on", "anything"} {
		if !autoSeedFromEnv(value) {
			t.Fatalf("%s=%q disabled autoseed", AutoSeedEnv, value)
		}
	}
	for _, value := range []string{"0", "false", "no", "off", " OFF ", "False"} {
		if autoSeedFromEnv(value) {
			t.Fatalf("%s=%q did not disable autoseed", AutoSeedEnv, value)
		}
	}
}

func TestEntityKeyMirrorsCatalogEntityIDs(t *testing.T) {
	movie := EntityKey(ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"})
	if movie != "movie:603" {
		t.Fatalf("movie key = %q", movie)
	}
	if got := catalogEntityKey("tmdb:movie:603"); got != movie {
		t.Fatalf("catalog key = %q, want %q", got, movie)
	}

	episode := EntityKey(ArchiveCoordinates{ContentKind: "episode", TMDBID: "1399", TMDBSeason: 1, TMDBEpisode: 2})
	if episode != "show:1399:s1:e2" {
		t.Fatalf("episode key = %q", episode)
	}
	if got := catalogEntityKey("tmdb:episode:show:1399:s1:e2"); got != episode {
		t.Fatalf("catalog key = %q, want %q", got, episode)
	}

	// Coordinates the relay could not publish have no key, so nothing can claim
	// or match them.
	for _, coords := range []ArchiveCoordinates{
		{ContentKind: "movie"},
		{ContentKind: "episode", TMDBID: "1399"},
		{ContentKind: "series", TMDBID: "1399"},
	} {
		if key := EntityKey(coords); key != "" {
			t.Fatalf("%+v has key %q", coords, key)
		}
	}
}

func newCatalogRelay(t *testing.T, body string, status int) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func TestCatalogHasEntityMatchesPublishedCoordinates(t *testing.T) {
	relay := newCatalogRelay(t, catalogBody, http.StatusOK)

	published, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if !published {
		t.Fatal("The Matrix is in the catalog but reported as absent")
	}

	absent, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "424"})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if absent {
		t.Fatal("a title the catalog does not list reported as published")
	}

	// Listed, but with no rendition to address: the stream endpoint could not
	// serve it, which is exactly the gap a seed fills.
	unservable, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "605"})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if unservable {
		t.Fatal("an entity with no addressable source reported as published")
	}
}

func TestCatalogHasEntityUsesSourceCoordinatesForOpaqueEntities(t *testing.T) {
	body := `{"entities":[{
	  "entityId":"3f66949c3f1d9fead2b43da629a0c5d43ae74b4eb46f03a70f625bfecdb7fb33",
	  "title":"Game of Thrones",
	  "sources":[{
	    "publicationId":"pub-opaque",
	    "renditionId":"rend-opaque",
	    "contentKind":"episode",
	    "mediaProvider":"tmdb",
	    "mediaId":"1399",
	    "seasonNumber":1,
	    "episodeNumber":2
	  }]
	}]}`
	relay := newCatalogRelay(t, body, http.StatusOK)
	published, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{
		ContentKind: "episode", TMDBID: "1399", TMDBSeason: 1, TMDBEpisode: 2,
	})
	if err != nil {
		t.Fatalf("CatalogHasEntity: %v", err)
	}
	if !published {
		t.Fatal("opaque entity source coordinates were reported as absent")
	}
}

// A relay that cannot answer must not be read as "the swarm does not have this".
// Turning a catalog failure into an absence is what would make every playback
// re-seed the same file.
func TestCatalogHasEntityReportsAnUnavailableRelayAsAnError(t *testing.T) {
	relay := newCatalogRelay(t, gateBody, http.StatusForbidden)

	published, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"})
	if err == nil {
		t.Fatal("a gated relay answered without an error")
	}
	if !IsRelayNotOpen(err) {
		t.Fatalf("error = %v, want the open-access gate", err)
	}
	if published {
		t.Fatal("a gated relay reported the title as published")
	}
}

// The same catalog read a search does, so a watch straight after a search costs
// no round trip — and a failed read is not retried on every heartbeat either.
func TestCatalogHasEntityReusesTheCachedCatalog(t *testing.T) {
	var reads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, apiPrefix+"/catalog") {
			reads++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(catalogBody))
	}))
	defer server.Close()
	relay, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for range 5 {
		if _, err := relay.CatalogHasEntity(t.Context(), ArchiveCoordinates{ContentKind: "movie", TMDBID: "603"}); err != nil {
			t.Fatalf("CatalogHasEntity: %v", err)
		}
	}
	if reads != 1 {
		t.Fatalf("catalog reads = %d, want 1", reads)
	}
}
