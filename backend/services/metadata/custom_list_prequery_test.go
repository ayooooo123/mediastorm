package metadata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"novastream/models"
)

// TestCustomListPreQueryEnrichesOnlyThePage checks that a PreQuery sort runs on
// the source rows before pagination: only the page's titles hit the metadata
// APIs, the totals reflect the whole list, and nothing is cached as the full list.
func TestCustomListPreQueryEnrichesOnlyThePage(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	listURL := "https://mdblist.com/lists/test/presort/json"
	source := []mdblistItem{
		{Rank: 1, Title: "Bravo", MediaType: "movie", TMDBID: ptr(int64(101)), ReleaseYear: 2003},
		{Rank: 2, Title: "Alpha", MediaType: "movie", TMDBID: ptr(int64(102)), ReleaseYear: 2010},
		{Rank: 3, Title: "Delta", MediaType: "movie", TMDBID: ptr(int64(103)), ReleaseYear: 1990},
		{Rank: 4, Title: "Charlie", MediaType: "movie", TMDBID: ptr(int64(104)), ReleaseYear: 2021},
		{Rank: 5, Title: "Echo", MediaType: "movie", TMDBID: ptr(int64(105)), ReleaseYear: 2015},
	}
	movieID := regexp.MustCompile(`/3/movie/(\d+)`)
	var mu sync.Mutex
	touched := map[string]bool{}
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		if req.URL.Host == "mdblist.com" {
			data, _ := json.Marshal(source)
			body = string(data)
		} else if m := movieID.FindStringSubmatch(req.URL.Path); m != nil {
			mu.Lock()
			touched[m[1]] = true
			mu.Unlock()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	svc := &Service{client: newTVDBClient("", "eng", httpc, 24), cache: cache, tmdb: newTMDBClient("test", "eng", httpc, cache)}

	var seen []models.TrendingItem
	yearDesc := func(rows []models.TrendingItem) []int {
		seen = rows
		order := make([]int, len(rows))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return rows[order[a]].Title.Year > rows[order[b]].Title.Year })
		return order
	}

	items, total, unfiltered, err := svc.GetCustomList(context.Background(), listURL, CustomListOptions{
		Limit: 2, Offset: 1, PreQuery: yearDesc, SuppressProgress: true, DeferArtwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(source) {
		t.Fatalf("PreQuery saw %d rows, want the whole list (%d)", len(seen), len(source))
	}
	if total != 5 || unfiltered != 5 {
		t.Fatalf("total=%d unfiltered=%d, want 5/5", total, unfiltered)
	}
	// Year desc: Charlie 2021, Echo 2015, Alpha 2010, Bravo 2003, Delta 1990.
	// Enrichment against the stub APIs blanks names and IDs; the source rank survives.
	var got []int
	for _, item := range items {
		got = append(got, item.Rank)
	}
	if want := []int{5, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("page ranks = %v, want %v", got, want)
	}
	if !touched["105"] || !touched["102"] {
		t.Fatalf("page movies were not enriched (touched %v)", touched)
	}
	for id := range touched {
		if id != "105" && id != "102" {
			t.Fatalf("enriched off-page movie %s (touched %v)", id, touched)
		}
	}
	var cached []models.TrendingItem
	for _, mode := range []string{"full", "lite", "fast"} {
		if ok, _ := cache.get(cacheKey("mdblist", "custom", "v8", mode, listURL, "eng"), &cached); ok {
			t.Fatalf("pre-queried page must not be cached as the full %s list", mode)
		}
	}
}

// TestCustomListPreQueryUsesCachedEnrichedRows checks that a warm full-list
// cache is ordered by PreQuery on its enriched rows before pagination.
func TestCustomListPreQueryUsesCachedEnrichedRows(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	listURL := "https://mdblist.com/lists/test/presort-warm/json"
	rows := []models.TrendingItem{
		{Rank: 1, Title: models.Title{ID: "a", Name: "B", Year: 2001, MediaType: "movie"}},
		{Rank: 2, Title: models.Title{ID: "b", Name: "A", Year: 2005, MediaType: "movie"}},
		{Rank: 3, Title: models.Title{ID: "c", Name: "C", Year: 1999, MediaType: "movie"}},
	}
	if err := cache.set(cacheKey("mdblist", "custom", "v8", "full", listURL, "eng"), rows); err != nil {
		t.Fatal(err)
	}
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	svc := &Service{client: newTVDBClient("", "eng", httpc, 24), cache: cache, tmdb: newTMDBClient("test", "eng", httpc, cache)}
	reverse := func(rows []models.TrendingItem) []int { return []int{2, 0} }
	items, total, unfiltered, err := svc.GetCustomList(context.Background(), listURL, CustomListOptions{Limit: 1, Offset: 1, PreQuery: reverse, DeferArtwork: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || unfiltered != 3 || len(items) != 1 || items[0].Title.ID != "a" {
		t.Fatalf("got items=%v total=%d unfiltered=%d", items, total, unfiltered)
	}
}

func TestSourceTitlesAreDisplayNames(t *testing.T) {
	for language, want := range map[string]bool{"": true, "eng": true, "en": true, "deu": false, "fra": false} {
		svc := &Service{client: newTVDBClient("", language, &http.Client{}, 24)}
		if got := svc.SourceTitlesAreDisplayNames(); got != want {
			t.Errorf("language %q: got %t, want %t", language, got, want)
		}
	}
}
