package handlers

import (
	"testing"

	"novastream/models"
)

type seriesReleaseMetadataService struct {
	fakeMetadataService
	releases map[int64][2]string
}

func (s *seriesReleaseMetadataService) GetCachedSeriesRelease(_ int64, tvdbID int64) (string, string) {
	r := s.releases[tvdbID]
	return r[0], r[1]
}

func TestEnrichDisplayListSeriesReleasesUsesCachedStatus(t *testing.T) {
	meta := &seriesReleaseMetadataService{releases: map[int64][2]string{
		1: {models.SeriesReleaseStatusUnreleased, "2099-12-25"},
		2: {models.SeriesReleaseStatusReleased, "2020-01-01"},
	}}
	items := []models.WatchlistItem{
		{ID: "a", MediaType: "series", ExternalIDs: map[string]string{"tvdb": "1"}},
		{ID: "b", MediaType: "series", Status: models.SeriesReleaseStatusUnreleased, ExternalIDs: map[string]string{"tvdb": "2"}},
		{ID: "c", MediaType: "series", Status: models.SeriesReleaseStatusReleased, ExternalIDs: map[string]string{"tvdb": "3"}},
		{ID: "d", MediaType: "movie", ExternalIDs: map[string]string{"tvdb": "1"}},
	}

	enrichDisplayListSeriesReleases(items, meta)

	if items[0].Status != models.SeriesReleaseStatusUnreleased || items[0].ReleaseDate != "2099-12-25" {
		t.Fatalf("unreleased series = %q %q", items[0].Status, items[0].ReleaseDate)
	}
	if items[1].Status != models.SeriesReleaseStatusReleased {
		t.Fatalf("a stale stored status should be replaced, got %q", items[1].Status)
	}
	if items[2].Status != models.SeriesReleaseStatusReleased || items[2].ReleaseDate != "" {
		t.Fatal("an uncached series keeps its stored status")
	}
	if items[3].Status != "" || items[3].ReleaseDate != "" {
		t.Fatal("movies are left to the release-window enrichment")
	}
}
