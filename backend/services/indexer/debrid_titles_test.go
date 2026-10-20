package indexer

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/services/debrid"
)

type bilingualDebrid struct {
	mu          sync.Mutex
	queries     []string
	failEnglish bool
}

func (s *bilingualDebrid) Search(_ context.Context, opts debrid.SearchOptions) ([]models.NZBResult, error) {
	s.mu.Lock()
	s.queries = append(s.queries, opts.Query)
	s.mu.Unlock()
	if strings.HasPrefix(opts.Query, "Moana") {
		if s.failEnglish {
			return nil, errors.New("English query failed")
		}
		return []models.NZBResult{{Title: "Moana.2016.1080p.WEB-DL.POLISH.AAC", GUID: "english-name", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"languages": "pl"}}}, nil
	}
	return []models.NZBResult{{Title: "Vaiana.2016.1080p.WEB-DL.ENGLISH.AAC", GUID: "polish-name", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"languages": "eng"}}}, nil
}

func TestDebridSearchesBothNamesAndRanksByAudio(t *testing.T) {
	for _, mode := range []string{"search", "split", "scored", "scored-split"} {
		t.Run(mode, func(t *testing.T) {
			mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			settings := config.DefaultSettings()
			settings.Metadata.Language = []string{"eng", "pol"}
			settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
			settings.Streaming.MaxAlternateTitleSearches = 1
			settings.Playback.PreferredAudioLanguage = "pol"
			if err := mgr.Save(settings); err != nil {
				t.Fatal(err)
			}
			provider := &bilingualDebrid{}
			svc := NewService(mgr, &preferredTitleMetadata{preferred: "Vaiana"}, provider)
			profile := &models.UserSettings{}
			profile.Metadata.PrimaryLanguage = "pol"
			profile.Playback.PreferredAudioLanguage = "pol"
			svc.SetUserSettingsProvider(staticUserSettingsProvider{settings: profile})
			opts := SearchOptions{Query: "Vaiana", MediaType: "movie", Year: 2016, UserID: "polish", AlternateTitles: []string{"Unrelated Alias", "Moana"}}
			results := runLanguageSearchMode(t, svc, opts, mode)
			if len(results) != 2 || results[0].GUID != "english-name" {
				t.Fatalf("want Polish audio under English title first, got %v", results)
			}
			if len(provider.queries) != 2 || !slices.Contains(provider.queries, "Vaiana") || !slices.Contains(provider.queries, "Moana 2016") {
				t.Fatalf("both nonempty queries should run within budget: %v", provider.queries)
			}
		})
	}
}

func TestDebridLanguageQueryFailureKeepsOtherResults(t *testing.T) {
	svc := &Service{debrid: &bilingualDebrid{failEnglish: true}}
	results, err := svc.searchDebridTitles(t.Context(), debrid.SearchOptions{Query: "Vaiana"}, SearchOptions{Query: "Vaiana", MediaType: "movie", Year: 2016}, []string{"Moana"})
	if err != nil || len(results) != 1 || results[0].Attributes["searchIncomplete"] != "true" {
		t.Fatalf("expected surviving result marked incomplete, got %v, %v", results, err)
	}
}

func TestDebridTitleDedupPreservesDifferentTorrentFiles(t *testing.T) {
	svc := &Service{debrid: stubDebridSearchService{results: []models.NZBResult{
		{GUID: "same-hash", Attributes: map[string]string{"fileIndex": "1"}},
		{GUID: "same-hash", Attributes: map[string]string{"fileIndex": "2"}},
	}}}
	results, err := svc.searchDebridTitles(t.Context(), debrid.SearchOptions{Query: "Vaiana"}, SearchOptions{Query: "Vaiana", MediaType: "movie"}, []string{"Moana"})
	if err != nil || len(results) != 2 {
		t.Fatalf("dedup must keep distinct files, got %v, %v", results, err)
	}
}

func TestDebridTitleDedupPreservesDifferentUntypedReleases(t *testing.T) {
	svc := &Service{debrid: stubDebridSearchService{results: []models.NZBResult{
		{Indexer: "Fixture", Title: "Moana.1080p.POL"},
		{Indexer: "Fixture", Title: "Moana.2160p.POL"},
	}}}
	results, err := svc.searchDebridTitles(t.Context(), debrid.SearchOptions{Query: "Vaiana"}, SearchOptions{Query: "Vaiana", MediaType: "movie"}, []string{"Moana"})
	if err != nil || len(results) != 2 {
		t.Fatalf("dedup must keep distinct releases from one indexer, got %v, %v", results, err)
	}
}

type recordingDebrid struct {
	mu      sync.Mutex
	queries map[string]string // query -> filter query
}

func (s *recordingDebrid) Search(_ context.Context, opts debrid.SearchOptions) ([]models.NZBResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries[opts.Query] = opts.FilterQuery
	return nil, nil
}

// Every search variant, including anime absolute ones, carries the requested
// episode query for filtering.
func TestDebridVariantsCarryFilterQuery(t *testing.T) {
	provider := &recordingDebrid{queries: map[string]string{}}
	svc := &Service{debrid: provider}
	base := debrid.SearchOptions{Query: "Dr STONE S01E01", MediaType: "series"}
	opts := SearchOptions{Query: "Dr STONE S01E01", MediaType: "series", IsAnime: true, AbsoluteEpisodeNumber: 1}
	if _, err := svc.searchDebridTitles(context.Background(), base, opts, []string{"Dr STONE"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.queries["Dr STONE 1"]; !ok {
		t.Fatalf("absolute variant not searched: %v", provider.queries)
	}
	for query, filterQuery := range provider.queries {
		if query != base.Query && filterQuery != base.Query {
			t.Errorf("variant %q filtered against %q, want %q", query, filterQuery, base.Query)
		}
	}
}
