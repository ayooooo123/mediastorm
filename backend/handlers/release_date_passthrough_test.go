package handlers

import (
	"testing"

	"novastream/models"
)

func TestWatchlistItemsToTrendingKeepsReleaseDate(t *testing.T) {
	items := []models.WatchlistItem{{ID: "tmdb:movie:1", MediaType: "movie", Name: "Dated", ReleaseDate: "2031-05-01"}}

	trending := watchlistItemsToTrending(items)

	if len(trending) != 1 || trending[0].Title.ReleaseDate != "2031-05-01" {
		t.Fatalf("expected releaseDate to carry over, got %+v", trending)
	}
}

func TestSlimTitlesKeepsReleaseDate(t *testing.T) {
	slim := slimTitles([]models.Title{{ID: "tmdb:movie:1", MediaType: "movie", ReleaseDate: "2031-05-01"}})

	if slim[0].ReleaseDate != "2031-05-01" {
		t.Fatalf("expected releaseDate to carry over, got %q", slim[0].ReleaseDate)
	}
}
