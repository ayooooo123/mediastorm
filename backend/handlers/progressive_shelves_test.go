package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"net/http/httptest"
	"testing"

	"novastream/models"
	"novastream/services/metadata"
)

type progressiveTestService struct {
	metadataService
	cards   int
	full    int
	hideOdd bool
}

func (s *progressiveTestService) MDBListIsEnabled() bool { return false }
func (s *progressiveTestService) GetCustomListSource(context.Context, string) ([]metadata.CuratedItem, error) {
	return nil, nil
}
func (s *progressiveTestService) GetShelfCards(_ context.Context, items []metadata.CuratedItem) ([]models.TrendingItem, error) {
	s.cards += len(items)
	out := make([]models.TrendingItem, len(items))
	for i, item := range items {
		out[i].Title = models.Title{ID: fmt.Sprint(item.TMDBID), Name: item.Title, TMDBID: item.TMDBID, MediaType: item.MediaType, Certification: "PG", Genres: item.Genres}
	}
	return out, nil
}
func (s *progressiveTestService) FilterShelfVisibility(_ context.Context, items []models.TrendingItem, movies, shows bool) []models.TrendingItem {
	result := []models.TrendingItem{}
	for _, item := range items {
		if !s.hideOdd || item.Title.TMDBID%2 == 0 {
			result = append(result, item)
		}
	}
	return result
}
func (s *progressiveTestService) GetCuratedList(_ context.Context, items []metadata.CuratedItem, _ string) ([]models.TrendingItem, error) {
	s.full += len(items)
	out := make([]models.TrendingItem, len(items))
	for i, item := range items {
		out[i].Title = models.Title{ID: "different", TMDBID: item.TMDBID, Name: item.Title, MediaType: item.MediaType, Genres: []string{"Drama"}}
	}
	return out, nil
}
func (s *progressiveTestService) EnrichTrendingCertifications(_ context.Context, items []models.TrendingItem) {
	for i := range items {
		if items[i].Title.TMDBID%2 != 0 {
			items[i].Title.Certification = "R"
		}
	}
}
func progressiveSource(n int) []metadata.CuratedItem {
	out := make([]metadata.CuratedItem, n)
	for i := range out {
		out[i] = metadata.CuratedItem{TMDBID: int64(i + 1), Title: fmt.Sprint(i + 1), MediaType: "movie"}
	}
	return out
}

func TestProgressiveShelfReadsOnlyVisiblePageThenCompletes(t *testing.T) {
	h := &MetadataHandler{}
	s := &progressiveTestService{hideOdd: true}
	source := progressiveSource(200)
	r := httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20", nil)
	first, err := h.progressiveCuratedShelf(r, s, source, "test", "", true, false, 20, 0)
	if err != nil || len(first.Items) != 20 || s.cards != 40 || s.full != 0 || !first.TotalPending || !first.MetadataPending {
		t.Fatalf("first=%+v cards=%d full=%d err=%v", first, s.cards, s.full, err)
	}
	if first.Items[0].Title.TMDBID != 2 || first.Items[19].Title.TMDBID != 40 {
		t.Fatal("filtering/source order changed")
	}
	r = httptest.NewRequest("GET", "/?shelfPhase=complete&limit=20", nil)
	full, err := h.progressiveCuratedShelf(r, s, source, "test", "", true, false, 20, 0)
	if err != nil || full.Total != 100 || full.TotalPending || full.MetadataPending || s.full != 20 {
		t.Fatalf("full=%+v hydrate=%d err=%v", full, s.full, err)
	}
	for i := range full.Items {
		if full.Items[i].Title.ID != first.Items[i].Title.ID {
			t.Fatal("background pass changed identity")
		}
	}
}

func TestProgressiveShelfFiltersHomeViewBeforePagination(t *testing.T) {
	h := &MetadataHandler{}
	s := &progressiveTestService{}
	source := progressiveSource(200)
	for i := range source {
		if i%2 == 0 {
			source[i].MediaType = "series"
		}
	}
	r := httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20&filterMediaType=movie", nil)
	first, err := h.progressiveCuratedShelf(r, s, source, "test", "", false, false, 20, 20)
	if err != nil || first.Total != 100 || first.TotalPending || len(first.Items) != 20 || first.Items[0].Title.TMDBID != 42 {
		t.Fatalf("wrong page: %+v %v", first, err)
	}
}

func TestProgressiveShelfAdvancedSortUsesEstablishedPath(t *testing.T) {
	for _, query := range []string{"shelfPhase=cards&limit=20&sortBy=rating", "shelfPhase=cards&limit=20&genres=Drama", "shelfPhase=cards&limit=0"} {
		if progressiveShelfRequest(httptest.NewRequest("GET", "/?"+query, nil)) {
			t.Fatalf("incomplete index used for %s", query)
		}
	}
	if !progressiveShelfRequest(httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20&filterMediaType=movie", nil)) {
		t.Fatal("media filter should use source index")
	}
}

// A large home row (high itemCap) uses the progressive path for both phases:
// its complete pass enriches the page rather than the whole list.
func TestProgressiveShelfLargeRowCardsOnly(t *testing.T) {
	if !progressiveShelfRequest(httptest.NewRequest("GET", "/?shelfPhase=cards&limit=500", nil)) {
		t.Fatal("large cards page should use the progressive path")
	}
	if !progressiveShelfRequest(httptest.NewRequest("GET", "/?shelfPhase=complete&limit=500", nil)) {
		t.Fatal("large complete page should use page-only enrichment")
	}
	if progressiveShelfRequest(httptest.NewRequest("GET", "/?shelfPhase=complete", nil)) {
		t.Fatal("an unbounded complete request keeps the full list path")
	}
}

// A 500-item complete page enriches exactly that page once and returns every
// title in source order with its card identity, as full enrichment would.
func TestProgressiveShelfLargeCompletePageEnrichesOnlyThePage(t *testing.T) {
	h := &MetadataHandler{}
	s := &progressiveTestService{}
	source := progressiveSource(600)
	r := httptest.NewRequest("GET", "/?shelfPhase=complete&limit=500", nil)
	resp, err := h.progressiveCuratedShelf(r, s, source, "test", "", false, false, 500, 0)
	if err != nil || len(resp.Items) != 500 || resp.Total != 600 || resp.MetadataPending || resp.TotalPending {
		t.Fatalf("resp items=%d total=%d err=%v", len(resp.Items), resp.Total, err)
	}
	if s.full != 500 || s.cards != 500 {
		t.Fatalf("enriched=%d cards=%d, want 500/500", s.full, s.cards)
	}
	for i, item := range resp.Items {
		if item.Title.TMDBID != int64(i+1) || item.Title.ID != fmt.Sprint(i+1) || item.Title.Genres[0] != "Drama" {
			t.Fatalf("item %d = %+v", i, item.Title)
		}
	}
}

func TestStremioCatalogItemKeepsCatalogMetadata(t *testing.T) {
	item := curatedItemFromStremioMeta(stremioMeta{ID: "tt1", Type: "movie", Name: "Catalog", Poster: "https://p/x.jpg", IMDBRating: "7.4"}, "movie")
	if !item.CatalogMetadata || item.IMDBRating != 7.4 || item.PosterURL != "https://p/x.jpg" {
		t.Fatalf("item = %+v", item)
	}
	for _, raw := range []interface{}{8.1, "8.1"} {
		if got := stremioIMDBRating(raw); got != 8.1 {
			t.Fatalf("rating(%v) = %v", raw, got)
		}
	}
	for _, raw := range []interface{}{nil, "", "n/a", 0.0, 11.0} {
		if got := stremioIMDBRating(raw); got != 0 {
			t.Fatalf("rating(%v) = %v, want 0", raw, got)
		}
	}
}

func TestProgressiveShelfKidsFilterBeforePagePublication(t *testing.T) {
	h := &MetadataHandler{UsersService: &fakeUsersServiceForSearch{users: map[string]models.User{"kid": {ID: "kid", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG"}}}}
	s := &progressiveTestService{}
	r := httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20", nil)
	resp, err := h.progressiveCuratedShelf(r, s, progressiveSource(100), "test", "kid", false, false, 20, 0)
	if err != nil || len(resp.Items) != 20 || s.cards != 40 {
		t.Fatalf("kids page=%+v cards=%d err=%v", resp, s.cards, err)
	}
	for _, item := range resp.Items {
		if item.Title.Certification != "PG" || item.Title.TMDBID%2 != 0 {
			t.Fatal("disallowed card published")
		}
	}
}

type progressiveHiddenTest struct{ hiddenItemsService }

func (progressiveHiddenTest) IsHidden(_ string, _ string, id string, _ map[string]string) bool {
	return id == "1" || id == "2"
}
func TestProgressiveShelfHiddenItemsBackfillAndExactTotals(t *testing.T) {
	filters := &progressiveShelfFilters{hidden: progressiveHiddenTest{}, hasHidden: true}
	r := httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20", nil)
	r = r.WithContext(context.WithValue(r.Context(), progressiveShelfFiltersKey{}, filters))
	h := &MetadataHandler{}
	s := &progressiveTestService{}
	resp, err := h.progressiveCuratedShelf(r, s, progressiveSource(25), "test", "user", false, false, 20, 0)
	if err != nil || len(resp.Items) != 20 || resp.Items[0].Title.ID != "3" || resp.Total != 23 || resp.TotalPending || !filters.applied {
		t.Fatalf("hidden filter/pagination: %+v %v", resp, err)
	}
}

type progressiveRouteService struct{ progressiveTestService }

func (s *progressiveRouteService) GetCustomListSource(context.Context, string) ([]metadata.CuratedItem, error) {
	return progressiveSource(200), nil
}
func TestProgressiveShelfDisplayRouteKeepsTotalAndPageSize(t *testing.T) {
	s := &progressiveRouteService{}
	h := &DisplayListHandler{MetadataHandler: &MetadataHandler{Service: s}, HiddenItemsService: paginationHiddenItemsService{}}
	r := httptest.NewRequest("GET", "/users/u/display-list?source=mdblist&url=https://mdblist.com/lists/test/test/json&shelfPhase=cards&limit=20&homeView=movies", nil)
	r = mux.SetURLVars(r, map[string]string{"userID": "u"})
	w := httptest.NewRecorder()
	h.Get(w, r)
	var result CustomListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.Items) != 20 || result.Total != 200 || !result.MetadataPending || s.cards != 20 {
		t.Fatalf("route lost pagination/flags: code=%d total=%d cards=%d items=%d pending=%v", w.Code, result.Total, s.cards, len(result.Items), result.MetadataPending)
	}
}

type progressiveTMDBTest struct {
	progressiveTestService
	pages int
}

func (s *progressiveTMDBTest) GetTMDBList(_ context.Context, opts metadata.TMDBListOptions) ([]models.TrendingItem, int, error) {
	s.pages++
	if !opts.DeferArtwork || opts.Limit > 20 {
		panic("blocking artwork or full index on home path")
	}
	out := []models.TrendingItem{}
	for i := opts.Offset; i < opts.Offset+opts.Limit && i < 200; i++ {
		out = append(out, models.TrendingItem{Title: models.Title{TMDBID: int64(i + 1), MediaType: "movie", Name: fmt.Sprint(i + 1), Poster: &models.Image{URL: "poster"}}})
	}
	return out, 200, nil
}
func TestProgressiveTMDBPagesStopWhenVisibleShelfIsFull(t *testing.T) {
	s := &progressiveTMDBTest{progressiveTestService: progressiveTestService{hideOdd: true}}
	h := &MetadataHandler{}
	r := httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20", nil)
	response, err := h.progressiveTMDBShelf(r, s, s, metadata.TMDBListOptions{}, "", true, false, 20, 0)
	if err != nil || len(response.Items) != 20 || s.pages != 2 || !response.TotalPending || response.Items[0].Title.TMDBID != 2 {
		t.Fatalf("TMDB paging: %+v pages=%d err=%v", response, s.pages, err)
	}
}

func TestProgressiveShelfFacetsCompleteWithoutEnrichingWholeList(t *testing.T) {
	h := &MetadataHandler{}
	s := &progressiveTestService{}
	source := progressiveSource(45)
	for i := range source {
		source[i].Genres = []string{"Drama"}
	}
	source[44].Title = "Zebra"
	source[44].Genres = []string{"Comedy"}
	first, err := h.progressiveCuratedShelf(httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20&includeFacets=true", nil), s, source, "test", "", false, false, 20, 0)
	if err != nil || s.cards != 20 || len(first.Genres) != 1 || first.Genres[0] != "Drama" {
		t.Fatalf("first page: %+v %v cards=%d", first, err, s.cards)
	}
	complete, err := h.progressiveCuratedShelf(httptest.NewRequest("GET", "/?shelfPhase=complete&limit=20&includeFacets=true", nil), s, source, "test", "", false, false, 20, 0)
	if err != nil || s.cards != 65 || s.full != 20 || len(complete.Genres) != 2 || len(complete.AlphabetBuckets) != 2 || complete.Total != 45 {
		t.Fatalf("background index: %+v %v cards=%d full=%d", complete, err, s.cards, s.full)
	}
}

func TestProgressiveTMDBFacetsReadRemainingSourcePagesInBackground(t *testing.T) {
	s := &progressiveTMDBTest{}
	h := &MetadataHandler{}
	first, err := h.progressiveTMDBShelf(httptest.NewRequest("GET", "/?shelfPhase=cards&limit=20&includeFacets=true", nil), s, s, metadata.TMDBListOptions{}, "", false, false, 20, 0)
	if err != nil || s.pages != 1 || first.Total != 200 {
		t.Fatalf("first=%+v pages=%d err=%v", first, s.pages, err)
	}
	s.pages = 0
	full, err := h.progressiveTMDBShelf(httptest.NewRequest("GET", "/?shelfPhase=complete&limit=20&includeFacets=true", nil), s, s, metadata.TMDBListOptions{}, "", false, false, 20, 0)
	if err != nil || s.pages != 10 || s.full != 20 || full.Total != 200 {
		t.Fatalf("complete=%+v pages=%d enriched=%d err=%v", full, s.pages, s.full, err)
	}
}
