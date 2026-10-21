package handlers

import (
	"context"
	"sync"
	"testing"

	"novastream/models"
)

type shelfLogoMetadataService struct {
	fakeMetadataService
	cached  map[int64]string
	fetched map[int64]string
	mu      sync.Mutex
	fetches int
}

func (s *shelfLogoMetadataService) GetCachedLogoURL(_ string, tmdbID int64, _ int64) string {
	return s.cached[tmdbID]
}

func (s *shelfLogoMetadataService) ShelfLogo(_ context.Context, _ string, tmdbID int64) *models.Image {
	s.mu.Lock()
	s.fetches++
	s.mu.Unlock()
	if url := s.fetched[tmdbID]; url != "" {
		return &models.Image{URL: url, Type: "logo"}
	}
	return nil
}

func TestFillMissingTitleLogosUsesCacheThenFetches(t *testing.T) {
	meta := &shelfLogoMetadataService{
		cached:  map[int64]string{1: "cached.png"},
		fetched: map[int64]string{2: "fetched.png"},
	}
	items := []models.TrendingItem{
		{Title: models.Title{TMDBID: 1, MediaType: "movie"}},
		{Title: models.Title{TMDBID: 2, MediaType: "series"}},
		{Title: models.Title{TMDBID: 3, MediaType: "movie", Logo: &models.Image{URL: "own.png"}}},
	}

	if !fillMissingTitleLogos(context.Background(), items, meta, true) {
		t.Fatal("deferred fill should report the uncached logo as pending")
	}
	if meta.fetches != 0 || items[0].Title.Logo == nil || items[0].Title.Logo.URL != "cached.png" || items[1].Title.Logo != nil {
		t.Fatalf("deferred fill should use the cache only; fetches=%d", meta.fetches)
	}

	if fillMissingTitleLogos(context.Background(), items, meta, false) {
		t.Fatal("complete fill should not report pending logos")
	}
	if meta.fetches != 1 || items[1].Title.Logo == nil || items[1].Title.Logo.URL != "fetched.png" {
		t.Fatalf("complete fill should fetch the missing logo; fetches=%d", meta.fetches)
	}
	if items[2].Title.Logo.URL != "own.png" {
		t.Fatal("an existing logo must be kept")
	}
}

func TestWatchlistItemsToTrendingKeepsLogo(t *testing.T) {
	items := watchlistItemsToTrending([]models.WatchlistItem{
		{ID: "tmdb:movie:603", MediaType: "movie", Name: "The Matrix", LogoURL: "https://example.test/logo.png"},
	})
	if items[0].Title.Logo == nil || items[0].Title.Logo.URL != "https://example.test/logo.png" {
		t.Fatalf("logo = %+v, want the item's logo", items[0].Title.Logo)
	}
}

func TestPersonalizedArtworkAddsLogos(t *testing.T) {
	meta := &shelfLogoMetadataService{fetched: map[int64]string{7: "fetched.png"}}
	items := []models.TrendingItem{{Title: models.Title{TMDBID: 7, MediaType: "movie",
		Backdrops: []models.Image{{URL: "one.jpg"}, {URL: "two.jpg"}}}}}
	enrichPersonalizedArtwork(context.Background(), items, meta, false)
	if items[0].Title.Logo == nil || items[0].Title.Logo.URL != "fetched.png" {
		t.Fatalf("logo = %+v, want fetched logo", items[0].Title.Logo)
	}
}
