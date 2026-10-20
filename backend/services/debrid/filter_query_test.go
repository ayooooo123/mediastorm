package debrid

import (
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/utils/filter"
)

// Anime absolute variants ("Dr STONE 1") must still be filtered against the
// requested episode, not the variant's own parse (no season or episode).
func TestSearchFiltersVariantAgainstFilterQuery(t *testing.T) {
	cfgManager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{{Name: "RealDebrid", Enabled: true, APIKey: "test-key"}}
	settings.TorrentScrapers = []config.TorrentScraperConfig{{Name: "Jackett", Type: "jackett", URL: "http://example.test", APIKey: "jackett-key", Enabled: true}}
	if err := cfgManager.Save(settings); err != nil {
		t.Fatal(err)
	}
	svc := NewSearchService(cfgManager, stubScraper{name: "Jackett", results: []ScrapeResult{
		{Title: "[sam] Dr. STONE - Science Future - S04E36 (WEB 1080p HEVC x265 10-bit EAC-3) [Dual-Audio]", Indexer: "Jackett", InfoHash: "1111111111111111111111111111111111111111"},
		{Title: "Dr.STONE.New.World.E01.MULTi.1080p.WEB.x264-AMB3R", Indexer: "Jackett", InfoHash: "2222222222222222222222222222222222222222"},
		{Title: "[SubsPlease] Dr. Stone - 01 (1080p)", Indexer: "Jackett", InfoHash: "3333333333333333333333333333333333333333"},
		{Title: "Dr.STONE.S01E01.1080p.WEB.x264", Indexer: "Jackett", InfoHash: "4444444444444444444444444444444444444444"},
	}})
	resolver := filter.NewSeriesEpisodeResolver(map[int]int{1: 24, 2: 11, 3: 22, 4: 37}).
		WithSeasonNames(map[int]string{1: "Season 1", 2: "Stone Wars", 3: "New World", 4: "Science Future"})

	results, err := svc.Search(t.Context(), SearchOptions{
		Query:                 "Dr STONE 1",
		FilterQuery:           "Dr STONE S01E01",
		MediaType:             "series",
		Year:                  2019,
		IsAnime:               true,
		AbsoluteEpisodeNumber: 1,
		EpisodeResolver:       resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, result := range results {
		titles = append(titles, result.Title)
		if result.Attributes["targetSeason"] != "1" || result.Attributes["targetEpisode"] != "1" {
			t.Errorf("%q: target S%sE%s, want S1E1", result.Title, result.Attributes["targetSeason"], result.Attributes["targetEpisode"])
		}
	}
	if len(results) != 2 || titles[0] != "[SubsPlease] Dr. Stone - 01 (1080p)" && titles[1] != "[SubsPlease] Dr. Stone - 01 (1080p)" {
		t.Fatalf("want the season 1 releases only, got %q", titles)
	}
}
