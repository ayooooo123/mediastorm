package debrid

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"novastream/config"
)

func TestAnthologyStreamIdentityScope(t *testing.T) {
	base := SearchRequest{TitleID: "tmdb:tv:299939", Query: "Monstre S01E01", Parsed: ParsedQuery{Title: "Monstre", MediaType: MediaTypeSeries, Season: 1, Episode: 1}}
	for _, tc := range []struct {
		name   string
		change func(*SearchRequest)
	}{
		{"other title", func(r *SearchRequest) { r.TitleID = "tmdb:tv:113988" }},
		{"no identity", func(r *SearchRequest) { r.TitleID = "" }},
		{"movie", func(r *SearchRequest) { r.Parsed.MediaType = MediaTypeMovie }},
		{"special", func(r *SearchRequest) { r.Parsed.Season = 0 }},
		{"another season", func(r *SearchRequest) { r.Parsed.Season = 2 }},
		{"season pack", func(r *SearchRequest) { r.Parsed.Episode = 0 }},
		{"unverified episode", func(r *SearchRequest) { r.Parsed.Episode = 9 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.change(&req)
			requests := mappedSearchRequests(req, true)
			got := requests[0]
			if got.IMDBID != req.IMDBID || got.Parsed.Season != req.Parsed.Season || got.Parsed.Episode != req.Parsed.Episode {
				t.Fatalf("unrelated request changed: %+v", got)
			}
		})
	}
	for episode := 1; episode <= 8; episode++ {
		req := base
		req.Parsed.Episode = episode
		requests := mappedSearchRequests(req, true)
		got := requests[0]
		if got.IMDBID != "tt13207736" || got.Parsed.Season != 4 || got.Parsed.Episode != episode || req.Parsed.Season != 1 || req.IMDBID != "" {
			t.Fatalf("incorrect mapping or mutated original: %+v -> %+v", req, got)
		}
	}
}

type anthologyResolverSpy struct{ called bool }

func (s *anthologyResolverSpy) ResolveIMDBID(context.Context, string, string, int) string {
	s.called = true
	return ""
}

type anthologyTextScraper struct {
	mu       sync.Mutex
	requests []SearchRequest
}

func (s *anthologyTextScraper) Name() string { return "text-search" }
func (s *anthologyTextScraper) Search(_ context.Context, req SearchRequest) ([]ScrapeResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	return nil, nil
}

func TestAnthologySearchUsesProviderCoordinates(t *testing.T) {
	for _, provider := range []string{"aiostreams", "torrentio"} {
		t.Run(provider, func(t *testing.T) {
			var paths []string
			client := newStubClient(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				return jsonResponse(http.StatusOK, `{"streams":[{"url":"https://example.test/video.mkv","infoHash":"0123456789012345678901234567890123456789","title":"Monster.The.Lizzie.Borden.Story.S04E02.1080p.WEB.mkv","behaviorHints":{"filename":"Monster.The.Lizzie.Borden.Story.S04E02.1080p.WEB.mkv"}}]}`), nil
			})
			var scraper Scraper = NewAIOStreamsScraper("https://example.test/manifest.json", "AIO", false, client)
			if provider == "torrentio" {
				scraper = NewTorrentioScraper(client, "", "Torrentio", "https://example.test")
			}
			cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			settings := config.DefaultSettings()
			settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
			settings.Streaming.DebridProviders = []config.DebridProviderSettings{{Name: "RealDebrid", Enabled: true, APIKey: "test"}}
			if err := cfg.Save(settings); err != nil {
				t.Fatal(err)
			}
			textScraper := &anthologyTextScraper{}
			svc := NewSearchService(cfg, scraper, textScraper)
			spy := &anthologyResolverSpy{}
			svc.SetIMDBResolver(spy)
			results, err := svc.Search(t.Context(), SearchOptions{TitleID: "tmdb:tv:299939", Query: "Monster: The Lizzie Borden Story S01E02", MediaType: "series", Year: 2026})
			if err != nil {
				t.Fatal(err)
			}
			if !spy.called {
				t.Fatal("catalog ID resolver must still be allowed to discover a separate IMDb identity")
			}
			if len(textScraper.requests) != 2 {
				t.Fatalf("expected catalog and provider text requests: %v", textScraper.requests)
			}
			if len(paths) != 1 || !strings.HasSuffix(paths[0], "/stream/series/tt13207736:4:2.json") {
				t.Fatalf("provider requests = %v", paths)
			}
			if len(results) != 1 {
				t.Fatalf("expected mapped S04 release to survive original-identity filtering, got %d", len(results))
			}
		})
	}
}

func TestDevilInSilverSearchPreservesSeparateIMDbAndQueriesParentSeason(t *testing.T) {
	var pathsMu sync.Mutex
	var paths []string
	client := newStubClient(func(r *http.Request) (*http.Response, error) {
		pathsMu.Lock()
		paths = append(paths, r.URL.Path)
		pathsMu.Unlock()
		if strings.Contains(r.URL.Path, "tt31186255") {
			return jsonResponse(http.StatusOK, `{"streams":[]}`), nil
		}
		return jsonResponse(http.StatusOK, `{"streams":[{"infoHash":"0123456789012345678901234567890123456789","title":"The.Terror.S03E06.1080p.WEB.mkv","behaviorHints":{"filename":"The.Terror.S03E06.1080p.WEB.mkv"}}]}`), nil
	})
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{{Name: "RealDebrid", Enabled: true, APIKey: "test"}}
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	text := &anthologyTextScraper{}
	svc := NewSearchService(cfg, NewTorrentioScraper(client, "", "Torrentio", "https://example.test"), text)
	results, err := svc.Search(t.Context(), SearchOptions{TitleID: "tmdb:tv:323903", Query: "The Terror: Devil in Silver S01E06",
		IMDBID: "tt31186255", MediaType: "series", Year: 2026, AlternateTitles: []string{"The Terror"}})
	if err != nil {
		t.Fatal(err)
	}
	// Catalog and parent identity searches run concurrently; arrival order is
	// not part of their contract.
	seenPaths := make(map[string]bool)
	for _, path := range paths {
		seenPaths[path] = true
	}
	if len(paths) != 2 || !seenPaths["/stream/series/tt31186255:1:6.json"] || !seenPaths["/stream/series/tt2708480:3:6.json"] {
		t.Fatalf("incorrect stream identities: %v", paths)
	}
	hasParentQuery := false
	for _, request := range text.requests {
		hasParentQuery = hasParentQuery || request.Query == "The Terror S03E06"
	}
	if len(text.requests) != 2 || !hasParentQuery {
		t.Fatalf("incorrect text requests: %+v", text.requests)
	}
	if len(results) != 1 || results[0].Attributes["targetSeason"] != "3" || results[0].Attributes["targetEpisode"] != "6" || results[0].Attributes["mappedCatalogEpisode"] != "S01E06" {
		t.Fatalf("incorrect result selection hints: %+v", results)
	}
}
