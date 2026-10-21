package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"novastream/models"
	"novastream/services/metadata"
)

// preQueryMetadataService mimics metadata.Service.GetCustomList: it applies
// PreQuery to the source rows, paginates, and "enriches" only the page.
type preQueryMetadataService struct {
	fakeMetadataService
	source   []models.TrendingItem
	enriched []string
}

func (f *preQueryMetadataService) SourceTitlesAreDisplayNames() bool { return true }

func (f *preQueryMetadataService) GetCustomList(_ context.Context, listURL string, opts metadata.CustomListOptions) ([]models.TrendingItem, int, int, error) {
	f.lastCustomListURL = listURL
	f.lastCustomListOptions = opts
	rows := append([]models.TrendingItem(nil), f.source...)
	if opts.PreQuery != nil {
		order := opts.PreQuery(rows)
		selected := make([]models.TrendingItem, 0, len(order))
		for _, idx := range order {
			selected = append(selected, rows[idx])
		}
		rows = selected
	}
	total := len(rows)
	rows = paginateTrendingItems(rows, opts.Offset, opts.Limit)
	page := make([]models.TrendingItem, len(rows))
	for i, row := range rows {
		f.enriched = append(f.enriched, row.Title.Name)
		page[i] = row
		page[i].Title.Genres = []string{"Genre " + row.Title.Name}
	}
	return page, total, len(f.source), nil
}

func preQueryTestSource() []models.TrendingItem {
	titles := []struct {
		name string
		year int
		kind string
	}{
		{"The Zebra", 2001, "movie"}, {"Apple", 1999, "series"}, {"Mango", 2010, "movie"},
		{"Banana", 2001, "movie"}, {"An Owl", 1985, "series"}, {"Cherry", 2010, "movie"},
		{"42", 2020, "movie"}, {"Date", 1999, "movie"},
	}
	rows := make([]models.TrendingItem, len(titles))
	for i, title := range titles {
		rows[i] = models.TrendingItem{Rank: i + 1, Title: models.Title{
			ID: fmt.Sprintf("tmdb:movie:%d", i+1), Name: title.name, Year: title.year, MediaType: title.kind,
		}}
	}
	return rows
}

func preQueryNames(items []models.TrendingItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Title.Name
	}
	return out
}

func TestCustomListSourceSortEnrichesOnlyThePage(t *testing.T) {
	cases := []string{
		"sortBy=year&sortDirection=desc",
		"sortBy=year&sortDirection=asc",
		"sortBy=name&sortDirection=asc",
		"sortBy=name&sortDirection=desc",
		"sortBy=added&sortDirection=desc",
		"filterMediaType=movie&sortBy=year&sortDirection=desc",
		"titleFilter=an&sortBy=name",
		"alphabet=C&sortBy=name",
	}
	for _, params := range cases {
		for _, offset := range []int{0, 3} {
			t.Run(fmt.Sprintf("%s/offset=%d", params, offset), func(t *testing.T) {
				fake := &preQueryMetadataService{source: preQueryTestSource()}
				handler := NewMetadataHandler(fake, testConfigManager(t))
				listURL := url.QueryEscape("https://mdblist.com/lists/snoak/disney-plus-top-10-movies/json")
				target := fmt.Sprintf("/api/lists/custom?url=%s&%s&includeFacets=true&limit=3&offset=%d", listURL, params, offset)
				req := httptest.NewRequest(http.MethodGet, target, nil)
				rec := httptest.NewRecorder()
				handler.CustomList(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				opts := fake.lastCustomListOptions
				if opts.PreQuery == nil || opts.Limit != 3 || opts.Offset != offset {
					t.Fatalf("expected a paged pre-query, got limit=%d offset=%d preQuery=%t", opts.Limit, opts.Offset, opts.PreQuery != nil)
				}
				var payload CustomListResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}

				// The full-enrichment path: query the whole list, then paginate.
				query := parseDisplayListQuery(req)
				full := query.Apply(preQueryTestSource())
				want := preQueryNames(paginateTrendingItems(full, offset, 3))
				if got := preQueryNames(payload.Items); !reflect.DeepEqual(got, want) {
					t.Fatalf("order = %v, want %v", got, want)
				}
				if payload.Total != len(full) {
					t.Fatalf("total = %d, want %d", payload.Total, len(full))
				}
				if len(fake.enriched) != len(want) || (len(want) > 0 && !reflect.DeepEqual(fake.enriched, want)) {
					t.Fatalf("enriched %v, want only the page %v", fake.enriched, want)
				}
				if wantAlphabet := displayListAlphabetBuckets(preQueryTestSource(), query); !reflect.DeepEqual(payload.AlphabetBuckets, wantAlphabet) {
					t.Fatalf("alphabet = %v, want %v", payload.AlphabetBuckets, wantAlphabet)
				}
			})
		}
	}
}

func TestCustomListEnrichedQueriesKeepFullList(t *testing.T) {
	for _, params := range []string{
		"sortBy=rating", "sortBy=duration", "genres=Drama", "watchStatus=watched&sortBy=year",
	} {
		t.Run(params, func(t *testing.T) {
			fake := &preQueryMetadataService{source: preQueryTestSource()}
			handler := NewMetadataHandler(fake, testConfigManager(t))
			req := httptest.NewRequest(http.MethodGet, "/api/lists/custom?url=https%3A%2F%2Fmdblist.com%2Flists%2Fa%2Fb%2Fjson&limit=3&"+params, nil)
			rec := httptest.NewRecorder()
			handler.CustomList(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			opts := fake.lastCustomListOptions
			if opts.PreQuery != nil || opts.Limit != 0 || opts.Offset != 0 {
				t.Fatalf("expected the full enriched list, got limit=%d offset=%d preQuery=%t", opts.Limit, opts.Offset, opts.PreQuery != nil)
			}
		})
	}
}

func TestDisplayListSourceQueryable(t *testing.T) {
	cases := []struct {
		query    displayListQueryOptions
		reliable bool
		want     bool
	}{
		{displayListQueryOptions{SortBy: "year"}, false, true},
		{displayListQueryOptions{SortBy: "added", MediaType: "movie"}, false, true},
		{displayListQueryOptions{SortBy: "name"}, true, true},
		{displayListQueryOptions{SortBy: "name"}, false, false},
		{displayListQueryOptions{Title: "x"}, false, false},
		{displayListQueryOptions{Alphabet: "A"}, true, true},
		{displayListQueryOptions{SortBy: "rating"}, true, false},
		{displayListQueryOptions{SortBy: "duration"}, true, false},
		{displayListQueryOptions{Genres: []string{"Drama"}}, true, false},
		{displayListQueryOptions{WatchStatus: "complete"}, true, false},
		{displayListQueryOptions{WatchStatus: "all", SortBy: "year"}, true, true},
	}
	for _, tc := range cases {
		if got := tc.query.SourceQueryable(tc.reliable); got != tc.want {
			t.Errorf("%+v reliable=%t: got %t, want %t", tc.query, tc.reliable, got, tc.want)
		}
	}
}
