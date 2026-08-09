package handlers

import (
	"encoding/json"
	"testing"

	"novastream/models"
)

func TestPrewarmDisplayListQuerySupportsPlayableHomeShelves(t *testing.T) {
	tests := []struct {
		shelf models.ShelfConfig
		want  string
	}{
		{shelf: models.ShelfConfig{ID: "watchlist"}, want: "watchlist"},
		{shelf: models.ShelfConfig{ID: "trending-tv"}, want: "trending"},
		{shelf: models.ShelfConfig{ID: "my-recommended"}, want: "personalized"},
		{shelf: models.ShelfConfig{ID: "custom-1", Type: "mdblist", ListURL: "https://mdblist.com/lists/example/list/json"}, want: "mdblist"},
		{shelf: models.ShelfConfig{ID: "tmdb-company", Type: "tmdb", TMDBSourceType: "production-company", TMDBSourceID: "420", TMDBMediaType: "movie", Sort: "popularity.desc", TMDBDiscoverQuery: "genres=28"}, want: "tmdb-list"},
	}
	for _, tt := range tests {
		query, ok := prewarmDisplayListQuery(tt.shelf)
		if !ok {
			t.Fatalf("shelf %+v was not supported", tt.shelf)
		}
		if got := query.Get("source"); got != tt.want {
			t.Fatalf("source=%q, want %q for shelf %+v", got, tt.want, tt.shelf)
		}
	}
	tmdbQuery, ok := prewarmDisplayListQuery(models.ShelfConfig{
		ID: "tmdb-company", Type: "tmdb", TMDBSourceType: "production-company",
		TMDBSourceID: "420", TMDBMediaType: "movie", Sort: "popularity.desc",
		TMDBDiscoverQuery: "genres=28",
	})
	if !ok || tmdbQuery.Get("sourceId") != "420" || tmdbQuery.Get("discoverQuery") != "genres=28" || tmdbQuery.Get("limit") != "25" {
		t.Fatalf("unexpected TMDB prewarm query: %v", tmdbQuery)
	}
	if _, ok := prewarmDisplayListQuery(models.ShelfConfig{ID: "calendar"}); ok {
		t.Fatal("calendar navigation shelf should not be prewarmable")
	}
	if _, ok := prewarmDisplayListQuery(models.ShelfConfig{ID: "permanent-prequeue"}); ok {
		t.Fatal("already-prequeued items should not be prewarmed again")
	}
}

func TestStartupDisplayListQuerySupportsPermanentPrequeueShelf(t *testing.T) {
	shelf := models.ShelfConfig{ID: "permanent-prequeue", Name: "Permanent Prequeue", Enabled: true}
	if !isStartupFetchableCustomShelf(shelf) {
		t.Fatal("permanent prequeue should be included in startup shelf fetching")
	}
	query, ok := startupDisplayListQueryForShelf(shelf, 20, false, "")
	if !ok || query.Get("source") != "permanent-prequeue" {
		t.Fatalf("unexpected startup query: ok=%v query=%v", ok, query)
	}
}

func TestDecodePrewarmShelfItemSupportsTrendingAndWatchlistShapes(t *testing.T) {
	trendingRaw, _ := json.Marshal(models.TrendingItem{Title: models.Title{
		ID: "tmdb:movie:1", Name: "Movie", MediaType: "movie", Year: 2024, IMDBID: "tt1", TMDBID: 1,
	}})
	trending, ok := decodePrewarmShelfItem(trendingRaw)
	if !ok || trending.TitleID != "tmdb:movie:1" || trending.ImdbID != "tt1" || trending.ExternalIDs["tmdbId"] != "1" {
		t.Fatalf("unexpected trending item: ok=%v item=%+v", ok, trending)
	}

	watchlistRaw, _ := json.Marshal(models.WatchlistItem{
		ID: "tvdb:series:2", Name: "Show", MediaType: "series", Year: 2020, ExternalIDs: map[string]string{"imdb": "tt2"},
	})
	watchlist, ok := decodePrewarmShelfItem(watchlistRaw)
	if !ok || watchlist.TitleID != "tvdb:series:2" || watchlist.ImdbID != "tt2" {
		t.Fatalf("unexpected watchlist item: ok=%v item=%+v", ok, watchlist)
	}
}
