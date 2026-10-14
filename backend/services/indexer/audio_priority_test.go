package indexer

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

func TestExplicitAudioLanguageOverridesAllRanking(t *testing.T) {
	english := models.NZBResult{Title: "Moana.2.2024.2160p.REMUX", ServiceType: models.ServiceTypeUsenet, PublishDate: time.Now(), Attributes: map[string]string{"languages": "eng", "countryMatch": "true", "episodeYearPriority": "true"}}
	polish := models.NZBResult{Title: "Moana.2.2024.720p", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"languages": "pl"}}
	ctx := ScoringContext{ExplicitAudioLanguage: "pol", PreferredLang: "pol", ServicePriority: config.StreamingServicePriorityUsenet, RankingCriteria: config.DefaultRankingCriteria()}
	if compareByRankingCriteria(polish, english, ctx) >= 0 {
		t.Fatal("explicit Polish did not beat service, quality, and identity ranking")
	}
	// A per-service list must not hide its language match behind a higher quality result.
	for _, split := range []bool{false, true} {
		bundle := effectiveRankingBundle{Default: ctx.RankingCriteria, Debrid: ctx.RankingCriteria, Usenet: ctx.RankingCriteria}
		if split {
			bundle.Debrid = []config.RankingCriterion{{ID: config.RankingSize, Enabled: true}}
		}
		results := []models.NZBResult{english, {Title: "Moana.2.2024.2160p", ServiceType: models.ServiceTypeDebrid}, polish}
		sortResultsByRankingBundle(results, ctx, bundle)
		if results[0].Title != polish.Title {
			t.Fatalf("split=%v: Polish not first: %v", split, results)
		}
	}
	results := []models.NZBResult{english, polish}
	sortResultsNewestReleaseFirst(results, "pol")
	if results[0].Title != polish.Title {
		t.Fatal("newest override defeated language")
	}
	_, breakdown := ScoreResult(polish, ctx)
	if len(breakdown) == 0 || breakdown[0].Criterion != "Preferred Audio Language" || breakdown[0].RankValue != 1 {
		t.Fatalf("missing first-priority explanation: %v", breakdown)
	}
	ctx.ExplicitAudioLanguage = ""
	if compareByRankingCriteria(english, polish, ctx) >= 0 {
		t.Fatal("inherited global language unexpectedly forced to first")
	}
}

func TestPolishAudioPrioritySearchPaths(t *testing.T) {
	for _, mode := range []string{"search", "split", "scored", "scored-split"} {
		for _, newest := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "/newest", false: "/ranking"}[newest], func(t *testing.T) {
				mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
				settings := config.DefaultSettings()
				settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
				settings.Display.BypassFilteringForAIOStreamsOnly = false
				settings.Ranking.NewestReleaseFirst = newest
				settings.Ranking.Criteria = []config.RankingCriterion{{ID: config.RankingResolution, Enabled: true}}
				if err := mgr.Save(settings); err != nil {
					t.Fatal(err)
				}
				const pol = "Moana.2.2024.720p.WEB-DL"
				svc := NewService(mgr, nil, stubDebridSearchService{results: []models.NZBResult{
					{Title: "Moana.2.2024.2160p.WEB-DL", ServiceType: models.ServiceTypeDebrid, PublishDate: time.Now(), Attributes: map[string]string{"languages": "eng"}},
					{Title: pol, ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"languages": "pl"}},
				}})
				profile := &models.UserSettings{}
				profile.Playback.PreferredAudioLanguage = "pol"
				svc.SetUserSettingsProvider(staticUserSettingsProvider{settings: profile})
				results := runLanguageSearchMode(t, svc, SearchOptions{Query: "Moana 2", Year: 2024, MediaType: "movie", UserID: "polish", MaxResults: 1, IncludeFiltered: true, IncludeScoreBreakdown: true}, mode)
				if len(results) == 0 || results[0].Title != pol {
					t.Fatalf("preferred language lost before cap: %+v", results)
				}
				if !strings.Contains(mode, "split") && len(results) != 1 {
					t.Fatalf("expected final result cap, got %d", len(results))
				}
			})
		}
	}
}

func TestExplicitAudioLanguageCascade(t *testing.T) {
	profile := &models.UserSettings{}
	svc := &Service{userSettings: staticUserSettingsProvider{settings: profile}}
	if got := svc.explicitAudioLanguage("profile", ""); got != "" {
		t.Fatalf("unset preference: %s", got)
	}
	profile.Playback.PreferredAudioLanguage = "pl-PL"
	if got := svc.explicitAudioLanguage("profile", ""); got != "pol" {
		t.Fatalf("profile: %s", got)
	}
	eng := "en"
	client := newMutableClientSettingsProvider(&models.ClientFilterSettings{PreferredAudioLanguage: &eng})
	svc.SetClientSettingsProvider(client)
	if got := svc.explicitAudioLanguage("profile", "device"); got != "eng" {
		t.Fatalf("device: %s", got)
	}
	empty := ""
	client.Set(&models.ClientFilterSettings{PreferredAudioLanguage: &empty})
	if got := svc.explicitAudioLanguage("profile", "device"); got != "" {
		t.Fatalf("cleared device: %s", got)
	}
}

func TestExplicitAudioPreferenceChangesCacheKey(t *testing.T) {
	profile := &models.UserSettings{}
	svc := &Service{userSettings: staticUserSettingsProvider{settings: profile}}
	settings := config.DefaultSettings()
	settings.Playback.PreferredAudioLanguage = "pol"
	key := func() string {
		return svc.searchCacheKey("test", SearchOptions{UserID: "profile"}, settings, nil, models.FilterSettings{}, effectiveFilterBundle{}, models.AnimeFilteringSettings{}, effectiveOverrides{}, nil, effectiveRankingBundle{})
	}
	inherited := key()
	profile.Playback.PreferredAudioLanguage = "pol"
	if inherited == key() {
		t.Fatal("explicit preference reused inherited ranking cache despite changed precedence")
	}
}

func TestExplicitAudioMatchingOriginalLanguageIsNotPinned(t *testing.T) {
	profile := &models.UserSettings{}
	profile.Playback.PreferredAudioLanguage = "eng"
	svc := &Service{userSettings: staticUserSettingsProvider{settings: profile}}
	// Untagged English scene releases must not sink below tagged foreign dubs.
	if got := svc.explicitAudioLanguageFor(SearchOptions{UserID: "profile", OriginalLanguage: "en"}); got != "" {
		t.Fatalf("original-language preference pinned: %q", got)
	}
	if got := svc.explicitAudioLanguageFor(SearchOptions{UserID: "profile", OriginalLanguage: "ja"}); got != "eng" {
		t.Fatalf("dub preference lost: %q", got)
	}
	if got := svc.explicitAudioLanguageFor(SearchOptions{UserID: "profile"}); got != "eng" {
		t.Fatalf("unknown original language dropped preference: %q", got)
	}
}
