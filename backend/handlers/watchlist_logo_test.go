package handlers

import (
	"testing"

	"novastream/models"
)

type logoMetadataService struct {
	fakeMetadataService
	logos map[string]string
}

func (s *logoMetadataService) GetCachedLogoURL(mediaType string, tmdbID int64, _ int64) string {
	if mediaType == "movie" && tmdbID == 603 {
		return s.logos["movie:603"]
	}
	return ""
}

func TestEnrichWatchlistArtworkAddsCachedLogo(t *testing.T) {
	meta := &logoMetadataService{logos: map[string]string{"movie:603": "https://example.test/logo.png"}}
	items := []models.WatchlistItem{
		{ID: "tmdb:movie:603", MediaType: "movie", Name: "The Matrix", ExternalIDs: map[string]string{"tmdb": "603"}},
		{ID: "tmdb:series:1", MediaType: "series", Name: "Uncached", ExternalIDs: map[string]string{"tmdb": "1"}},
	}

	enrichWatchlistArtwork(items, meta)

	if items[0].LogoURL != "https://example.test/logo.png" {
		t.Fatalf("logo = %q, want cached logo", items[0].LogoURL)
	}
	if items[1].LogoURL != "" {
		t.Fatalf("uncached logo = %q, want empty", items[1].LogoURL)
	}
}
