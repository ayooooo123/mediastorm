package metadata

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func jsonResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestPublicMDBListJSONIDBecomesTMDBID(t *testing.T) {
	svc, _ := shelfTestService(t)
	svc.client.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":858024,"rank":1,"adult":0,"title":"Hamnet","tvdbid":359371,"imdb_id":"tt14905854","mediatype":"movie","release_year":2025}]`, nil), nil
	})}
	items, err := svc.GetCustomListSource(context.Background(), "https://mdblist.com/lists/test/public/json")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if items[0].TMDBID != 858024 || items[0].TVDBID != 359371 || items[0].IMDBID != "tt14905854" {
		t.Fatalf("ids not carried: %+v", items[0])
	}
	raw, err := svc.cachedCustomListSource(context.Background(), "https://mdblist.com/lists/test/public/json")
	if err != nil || raw[0].ID != 858024 {
		t.Fatalf("raw ID changed: %+v err=%v", raw, err)
	}
}

func TestMDBListAPIListPath(t *testing.T) {
	cases := map[string]string{
		"https://mdblist.com/lists/godver3/trending-movies/json": "lists/godver3/trending-movies",
		"https://mdblist.com/lists/godver3/trending-movies":      "lists/godver3/trending-movies",
		"https://mdblist.com/lists/12345":                        "lists/12345",
	}
	for input, want := range cases {
		if got, ok := mdblistAPIListPath(input); !ok || got != want {
			t.Errorf("%s => %q %v, want %q", input, got, ok, want)
		}
	}
	for _, input := range []string{"https://example.com/lists/a/b/json", "https://mdblist.com/lists/user", "https://mdblist.com/toplists/a/b"} {
		if got, ok := mdblistAPIListPath(input); ok {
			t.Errorf("%s unexpectedly mapped to %q", input, got)
		}
	}
}

func mdblistAPITestService(t *testing.T, handler func(*http.Request) *http.Response) (*Service, func() map[string]int) {
	t.Helper()
	svc, counts := shelfTestService(t)
	svc.ratingsCache = newFileCache(t.TempDir(), 168)
	svc.mdblist = newMDBListClient("list-key", nil, false, 24)
	svc.mdblist.listHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return handler(r), nil
	})}
	return svc, counts
}

func TestMDBListAPISourceParsesPagesAndSkipsPerItemFetches(t *testing.T) {
	listURL := "https://mdblist.com/lists/someone/api-source-" + t.Name() + "/json"
	var mu sync.Mutex
	var offsets []string
	svc, counts := mdblistAPITestService(t, func(r *http.Request) *http.Response {
		if r.Header.Get("User-Agent") != "mediastorm/1.0" {
			t.Errorf("missing User-Agent: %q", r.Header.Get("User-Agent"))
		}
		q := r.URL.Query()
		if r.URL.Host != "api.mdblist.com" || !strings.HasPrefix(r.URL.Path, "/lists/someone/api-source-") || !strings.HasSuffix(r.URL.Path, "/items") ||
			q.Get("apikey") != "list-key" || q.Get("limit") != "1000" || q.Get("append_to_response") != "poster,description,genres,ratings" {
			t.Errorf("unexpected request %s", r.URL)
		}
		mu.Lock()
		offsets = append(offsets, q.Get("offset"))
		mu.Unlock()
		header := make(http.Header)
		if q.Get("offset") == "0" {
			header.Set("X-Has-More", "true")
			return jsonResponse(200, `{"movies":[
				{"id":858024,"mediatype":"movie","imdb_id":"tt14905854","tvdb_id":359371,"ids":{"imdb":"tt14905854","tmdb":858024,"tvdb":359371},"title":"Hamnet","release_year":2025,"release_date":"2025-11-26","runtime":125,"poster":"https://image.tmdb.org/t/p/w200/hamnet.jpg","description":"A story.","genres":["drama","science-fiction"],"ratings":[{"source":"imdb","value":7.8,"score":78,"votes":139656},{"source":"mdblist","value":null,"score":80,"votes":null}],"rank":1},
				{"id":3,"mediatype":"movie","imdb_id":"tt3","ids":{"tmdb":3},"title":"Third","release_year":2024,"poster":"https://image.tmdb.org/t/p/w200/third.jpg","rank":3}],
				"shows":[{"id":2,"mediatype":"show","imdb_id":"tt2","ids":{"tmdb":2,"tvdb":22},"title":"Second","poster":"https://image.tmdb.org/t/p/w200/second.jpg","genres":["tv-movie"],"rank":2}],
				"seasons":[],"episodes":[]}`, header)
		}
		header.Set("X-Has-More", "false")
		return jsonResponse(200, `{"movies":[{"id":4,"mediatype":"movie","ids":{"tmdb":4},"title":"Fourth","poster":"https://image.tmdb.org/t/p/w200/fourth.jpg","rank":4}],"shows":[]}`, header)
	})
	svc.client.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("public JSON fetched despite API key: %s", r.URL)
		return jsonResponse(500, "", nil), nil
	})}

	items, err := svc.GetCustomListSource(context.Background(), listURL)
	if err != nil || len(items) != 4 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if strings.Join(offsets, ",") != "0,1000" {
		t.Fatalf("pagination offsets = %v", offsets)
	}
	for i, want := range []string{"Hamnet", "Second", "Third", "Fourth"} {
		if items[i].Title != want {
			t.Fatalf("rank order broken at %d: %+v", i, items)
		}
	}
	first := items[0]
	if first.TMDBID != 858024 || first.TVDBID != 359371 || first.IMDBID != "tt14905854" || first.MediaType != "movie" {
		t.Fatalf("ids: %+v", first)
	}
	if first.PosterURL != "https://image.tmdb.org/t/p/w342/hamnet.jpg" || first.Overview != "A story." || first.RuntimeMinutes != 125 {
		t.Fatalf("metadata: %+v", first)
	}
	if strings.Join(first.Genres, "|") != "Drama|Science Fiction" || items[1].Genres[0] != "TV Movie" {
		t.Fatalf("genres: %v %v", first.Genres, items[1].Genres)
	}
	if items[1].MediaType != "series" || items[1].TMDBID != 2 || items[1].TVDBID != 22 {
		t.Fatalf("show: %+v", items[1])
	}

	// List ratings satisfy list enrichment but never stand in for full ratings.
	ratings := svc.GetMDBListAllRatingsCached("tt14905854", "movie")
	if len(ratings) != 1 || ratings[0].Source != "imdb" || ratings[0].Value != 7.8 || ratings[0].Max != 10 {
		t.Fatalf("seeded ratings: %+v", ratings)
	}
	if svc.mdblistFullRatingsCached("tt14905854", "movie") != nil {
		t.Fatal("list ratings leaked into the full ratings cache")
	}
	if svc.GetMDBListAllRatingsCached("tt3", "movie") != nil {
		t.Fatal("ratings fabricated for an item without list ratings")
	}

	cards, err := svc.GetShelfCards(context.Background(), items)
	if err != nil || len(cards) != 4 {
		t.Fatalf("cards=%d err=%v", len(cards), err)
	}
	if len(counts()) != 0 {
		t.Fatalf("cards issued per-item TMDB requests: %v", counts())
	}
	if cards[0].Title.Poster == nil || cards[0].Title.Poster.URL != first.PosterURL || cards[0].Title.RuntimeMinutes != 125 {
		t.Fatalf("card metadata: %+v", cards[0].Title)
	}

	// The source cache serves the next request without another API call.
	if _, err := svc.GetCustomListSource(context.Background(), listURL); err != nil || len(offsets) != 2 {
		t.Fatalf("source cache missed: offsets=%v err=%v", offsets, err)
	}
}

func TestMDBListAPISourceFallsBackToPublicJSON(t *testing.T) {
	listURL := "https://mdblist.com/lists/someone/fallback-" + t.Name() + "/json"
	apiCalls := 0
	svc, _ := mdblistAPITestService(t, func(r *http.Request) *http.Response {
		apiCalls++
		return jsonResponse(403, "forbidden", nil)
	})
	svc.client.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":7,"title":"Public","imdb_id":"tt7","mediatype":"movie"}]`, nil), nil
	})}
	for i := 0; i < 2; i++ {
		items, err := svc.GetCustomListSource(context.Background(), listURL)
		if err != nil || len(items) != 1 || items[0].TMDBID != 7 {
			t.Fatalf("fallback items=%+v err=%v", items, err)
		}
	}
	if apiCalls != 1 {
		t.Fatalf("failed API source retried within the failure window: %d calls", apiCalls)
	}
}

func TestEnrichCustomListItemUsesTMDBBeforeTVDBSearch(t *testing.T) {
	svc, counts := shelfTestService(t)
	// A configured TVDB key would previously trigger title searches first;
	// shelfTestService fails the test on any non-TMDB request.
	svc.client = newTVDBClient("tvdb-key", "eng", svc.client.httpc, 24)
	movieID, seriesID := int64(7), int64(8)
	movie := svc.enrichCustomListItem(context.Background(), mdblistItem{Title: "Movie 7", IMDBID: "tt7", TMDBID: &movieID, MediaType: "movie"}, true)
	if movie.Title.TMDBID != 7 || movie.Title.Poster == nil || movie.Title.IMDBID != "tt7" {
		t.Fatalf("movie not hydrated from TMDB: %+v", movie.Title)
	}
	series := svc.enrichCustomListItem(context.Background(), mdblistItem{Title: "Series", TMDBID: &seriesID, MediaType: "show"}, true)
	if series.Title.TMDBID != 8 || series.Title.Poster == nil {
		t.Fatalf("series not hydrated from TMDB: %+v", series.Title)
	}
	if counts()["/3/movie/7"] != 1 || counts()["/3/tv/8"] != 1 {
		t.Fatalf("unexpected TMDB calls: %v", counts())
	}
}

func TestSearchTVDBCachesEmptyResults(t *testing.T) {
	searches := 0
	httpc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v4/search" {
			searches++
		}
		return jsonResponse(200, `{"data":[]}`, nil), nil
	})}
	svc := &Service{client: newTVDBClient("tvdb-key", "eng", httpc, 24), cache: newFileCache(t.TempDir(), 24)}
	svc.client.token = "token"
	svc.client.tokenExpiry = time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		if results, err := svc.searchTVDBMovie("Nothing", 2020, "tt0"); err != nil || len(results) != 0 {
			t.Fatalf("results=%v err=%v", results, err)
		}
		if results, err := svc.searchTVDBSeries("Nothing", 2020, "tt0"); err != nil || len(results) != 0 {
			t.Fatalf("results=%v err=%v", results, err)
		}
	}
	if searches != 2 {
		t.Fatalf("empty searches not cached: %d requests", searches)
	}
}
