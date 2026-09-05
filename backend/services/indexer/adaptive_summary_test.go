package indexer

import (
	"novastream/config"
	"novastream/models"
	"path/filepath"
	"testing"
	"time"
)

func TestAdaptiveSearchSummaryCountsBeforePresentationLimit(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.Filtering.AdaptivePlaybackEnabled = true
	settings.Filtering.AdaptiveTargetBufferFactor = 0.7
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	svc := NewService(mgr, nil, stubDebridSearchService{results: []models.NZBResult{
		{Title: "Widows.Bay.S01E07.1080p.WEB-DL", SizeBytes: 100_000_000, ServiceType: models.ServiceTypeDebrid},
		{Title: "Widows.Bay.S01E07.2160p.WEB-DL", SizeBytes: 8_000_000_000, ServiceType: models.ServiceTypeDebrid},
		{Title: "Widows.Bay.S01E07.2160p.HDR.WEB-DL", SizeBytes: 9_000_000_000, ServiceType: models.ServiceTypeDebrid},
		{Title: "Other.Show.S01E07.2160p.WEB-DL", SizeBytes: 9_000_000_000, ServiceType: models.ServiceTypeDebrid},
	}})
	summary := &models.AdaptiveSearchSummary{}
	results, err := svc.SearchWithScoring(t.Context(), SearchOptions{Query: "Widows Bay S01E07", MediaType: "series", IncludeFiltered: true, MaxResults: 1, AdaptiveThroughput: &models.AdaptiveThroughputContext{MeasuredMbps: 2.4, MeasuredAt: time.Now().Unix()}, AdaptiveSummary: summary})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d displayed results", len(results))
	}
	if !summary.Enabled || summary.MaxSizeGB == nil || *summary.MaxSizeGB != 0.6 || summary.FilteredCount != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestAdaptiveSearchSummaryUnavailableAndDisabled(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Filtering.AdaptivePlaybackEnabled = true
	for _, tc := range []struct {
		name    string
		enabled bool
		age     time.Duration
		wantCap bool
	}{
		{"fresh", true, time.Minute, true}, {"expired", true, 25 * time.Hour, false}, {"disabled", false, time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := buildAdaptiveSearchSummary(models.FilterSettings{AdaptivePlaybackEnabled: models.BoolPtr(tc.enabled)}, settings, SearchOptions{MediaType: "movie", AdaptiveThroughput: &models.AdaptiveThroughputContext{MeasuredMbps: 110, MeasuredAt: time.Now().Add(-tc.age).Unix()}})
			if (s.MaxSizeGB != nil) != tc.wantCap || s.Enabled != tc.enabled {
				t.Fatalf("unexpected summary: %+v", s)
			}
		})
	}
}
