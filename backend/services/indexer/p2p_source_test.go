package indexer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/services/peartube"
)

const p2pCatalogBody = `{
  "entities": [
    {"entityId": "tmdb:movie:603", "entityKind": "movie", "title": "The Matrix", "year": 1999,
     "sources": [{"publicationId": "pub-1", "publisherId": "abcdef0123456789", "renditionId": "rend-1", "byteLength": 4096}]}
  ],
  "nextCursor": null
}`

func newP2PService(t *testing.T, debridResults []models.NZBResult) *Service {
	t.Helper()
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/catalog" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, p2pCatalogBody)
	}))
	t.Cleanup(relayServer.Close)

	relay, err := peartube.New(relayServer.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, nil, stubDebridSearchService{results: debridResults})
	svc.SetPearTubeRelay(relay)
	return svc
}

// A p2p source has to survive the whole aggregation path — parallel launch,
// per-service ranking, merge — and arrive beside the debrid results, not
// instead of them.
func TestSearchAggregatesP2PAlongsideDebrid(t *testing.T) {
	svc := newP2PService(t, []models.NZBResult{
		{Title: "The.Matrix.1999.2160p.WEB-DL", Indexer: "Comet", ServiceType: models.ServiceTypeDebrid},
	})

	results, err := svc.Search(t.Context(), SearchOptions{
		Query:     "The Matrix 1999",
		MediaType: "movie",
		Year:      1999,
		TMDBID:    "603",
	})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}

	var p2p, debrid int
	var stream string
	for _, result := range results {
		switch result.ServiceType {
		case models.ServiceTypeP2P:
			p2p++
			stream = result.DownloadURL
		case models.ServiceTypeDebrid:
			debrid++
		}
	}
	if debrid != 1 {
		t.Fatalf("debrid results = %d, want 1 (p2p must not displace other sources)", debrid)
	}
	if p2p != 1 {
		t.Fatalf("p2p results = %d, want 1: %+v", p2p, results)
	}
	if want := relayStreamPath; stream[len(stream)-len(want):] != want {
		t.Fatalf("p2p stream URL = %q, want it to end in %q", stream, want)
	}
}

const relayStreamPath = "/api/v1/stream/pub-1/rend-1"

// With no relay configured the pipeline must behave exactly as it did before.
func TestSearchWithoutRelayEmitsNoP2PResults(t *testing.T) {
	svc := newP2PService(t, []models.NZBResult{
		{Title: "The.Matrix.1999.2160p.WEB-DL", Indexer: "Comet", ServiceType: models.ServiceTypeDebrid},
	})
	svc.SetPearTubeRelay(nil)

	results, err := svc.Search(t.Context(), SearchOptions{
		Query:     "The Matrix 1999",
		MediaType: "movie",
		Year:      1999,
		TMDBID:    "603",
	})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	for _, result := range results {
		if result.ServiceType == models.ServiceTypeP2P {
			t.Fatalf("p2p result appeared with no relay configured: %+v", result)
		}
	}
}

// A relay that is down must degrade to the other sources rather than failing
// the whole search.
func TestSearchSurvivesUnreachableRelay(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()

	svc := newP2PService(t, []models.NZBResult{
		{Title: "The.Matrix.1999.2160p.WEB-DL", Indexer: "Comet", ServiceType: models.ServiceTypeDebrid},
	})
	relay, err := peartube.New(dead.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}
	svc.SetPearTubeRelay(relay)

	results, err := svc.Search(t.Context(), SearchOptions{Query: "The Matrix 1999", MediaType: "movie", Year: 1999})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if len(results) != 1 || results[0].ServiceType != models.ServiceTypeDebrid {
		t.Fatalf("expected the debrid result to survive a dead relay, got %+v", results)
	}
}
