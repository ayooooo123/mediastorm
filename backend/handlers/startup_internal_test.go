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

func TestStartupStremioShelfQuery(t *testing.T) {
	shelf := models.ShelfConfig{
		ID:               "stremio-ranked",
		Type:             "stremio",
		Name:             "Ranked",
		AddonManifestURL: "https://addon.example/config/manifest.json",
		AddonCatalogType: "movie",
		AddonCatalogID:   "ranked",
	}
	query, ok := startupDisplayListQueryForShelf(shelf, 24, true, "client")
	if !ok {
		t.Fatal("expected startup Stremio shelf query")
	}
	if query.Get("source") != "stremio" ||
		query.Get("manifestUrl") != shelf.AddonManifestURL ||
		query.Get("catalogType") != "movie" ||
		query.Get("catalogId") != "ranked" {
		t.Fatalf("unexpected Stremio query: %v", query)
	}
	if query.Get("hideWatched") != "true" || query.Get("limit") != "28" {
		t.Fatalf("missing shared shelf options: %v", query)
	}
}

func TestStartupTMDBShelfFetchLimitPreservesOtherOverflow(t *testing.T) {
	tests := []struct {
		name        string
		shelfLimit  int
		wantLimit   string
		wantArtwork string
	}{
		{name: "default", wantLimit: "25", wantArtwork: "24"},
		{name: "smaller explicit limit", shelfLimit: 10, wantLimit: "10", wantArtwork: "10"},
		{name: "larger explicit limit", shelfLimit: 500, wantLimit: "25", wantArtwork: "24"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, ok := startupDisplayListQueryForShelf(models.ShelfConfig{
				ID:             "tmdb-company",
				Type:           "tmdb",
				TMDBSourceType: "production-company",
				TMDBSourceID:   "420",
				Limit:          test.shelfLimit,
			}, defaultStartupShelfLimit, false, "")
			if !ok {
				t.Fatal("expected startup TMDB shelf query")
			}
			if got := query.Get("limit"); got != test.wantLimit {
				t.Fatalf("limit = %q, want %q", got, test.wantLimit)
			}
			if got := query.Get("artworkLimit"); got != test.wantArtwork {
				t.Fatalf("artworkLimit = %q, want %q", got, test.wantArtwork)
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
