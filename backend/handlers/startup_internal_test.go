package handlers

import (
	"testing"

	"novastream/models"
)

func TestStartupDiscoverShelfRequestsArtworkForVisibleItems(t *testing.T) {
	tests := []models.ShelfConfig{
		{ID: "genre-16-movie", Type: "genre", Name: "Animation"},
		{ID: "decade-1990-tv", Type: "decade", Name: "1990s TV"},
	}

	for _, shelf := range tests {
		t.Run(shelf.Type, func(t *testing.T) {
			query, ok := startupDisplayListQueryForShelf(shelf, 24, false, "")
			if !ok {
				t.Fatal("expected startup shelf query")
			}
			if got := query.Get("limit"); got != "50" {
				t.Fatalf("limit = %q, want 50", got)
			}
			if got := query.Get("artworkLimit"); got != "28" {
				t.Fatalf("artworkLimit = %q, want 28", got)
			}
			if got := query.Get("lite"); got != "true" {
				t.Fatalf("lite = %q, want true", got)
			}
		})
	}
}
