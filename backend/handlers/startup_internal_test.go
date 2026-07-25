package handlers

import (
	"net/http/httptest"
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

func TestWatchSupportsNativeTMDBShelves(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		header  string
		support bool
	}{
		{name: "legacy Watch", url: "/startup", support: false},
		{name: "query capability", url: "/startup?nativeTMDBShelves=true", support: true},
		{name: "feature header", url: "/startup", header: "calendar-v2, tmdb-shelves", support: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", test.url, nil)
			request.Header.Set("X-Mediastorm-Features", test.header)
			if got := watchSupportsNativeTMDBShelves(request); got != test.support {
				t.Fatalf("watchSupportsNativeTMDBShelves() = %v, want %v", got, test.support)
			}
		})
	}
}
