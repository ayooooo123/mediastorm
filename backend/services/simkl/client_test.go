package simkl

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestScrobbleStartSendsHeadersAndBody(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/scrobble/start" {
				t.Fatalf("path = %q, want /scrobble/start", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"progress":12.5`) {
				t.Fatalf("body missing progress: %s", string(body))
			}
			if !strings.Contains(string(body), `"imdb":"tt1375666"`) {
				t.Fatalf("body missing imdb id: %s", string(body))
			}
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(`{"id":0,"action":"start","progress":12.5}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	resp, err := client.ScrobbleStart("client-id", "token", ScrobbleRequest{
		Progress: 12.5,
		Movie: &Movie{
			Title: "Inception",
			Year:  2010,
			IDs:   IDs{IMDB: "tt1375666", TMDB: 27205},
		},
	})
	if err != nil {
		t.Fatalf("ScrobbleStart() error = %v", err)
	}
	if resp.Action != "start" || resp.Progress != 12.5 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestExchangeCodeSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/oauth/token" {
				t.Fatalf("path = %q, want /oauth/token", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"redirect_uri":"urn:ietf:wg:oauth:2.0:oob"`) {
				t.Fatalf("body missing redirect uri: %s", string(body))
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"bearer"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	token, err := client.ExchangeCode("client-id", "secret", "urn:ietf:wg:oauth:2.0:oob", "code")
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if token.AccessToken != "token" {
		t.Fatalf("access token = %q", token.AccessToken)
	}
}

func TestStartPINAuthSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/oauth/pin" {
				t.Fatalf("path = %q, want /oauth/pin", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"user_code":"ABCD","verification_url":"https://simkl.com/pin/ABCD","expires_in":600,"interval":5}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	pin, err := client.StartPINAuth("client-id")
	if err != nil {
		t.Fatalf("StartPINAuth() error = %v", err)
	}
	if pin.UserCode != "ABCD" || pin.VerificationURL != "https://simkl.com/pin/ABCD" || pin.Interval != 5 {
		t.Fatalf("pin response = %+v", pin)
	}
}

func TestCheckPINAuthSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/oauth/pin/ABCD" {
				t.Fatalf("path = %q, want /oauth/pin/ABCD", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("simkl-api-key"); got != "client-id" {
				t.Fatalf("simkl-api-key = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != userAgent {
				t.Fatalf("User-Agent = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"token","user_id":"simkl-user"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	token, err := client.CheckPINAuth("client-id", "ABCD")
	if err != nil {
		t.Fatalf("CheckPINAuth() error = %v", err)
	}
	if token.AccessToken != "token" || token.UserID != "simkl-user" {
		t.Fatalf("pin token response = %+v", token)
	}
}

func TestGetActivitiesSendsRequiredQueryAndHeaders(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/activities" {
				t.Fatalf("path = %q, want /sync/activities", r.URL.Path)
			}
			if got := r.URL.Query().Get("client_id"); got != "client-id" {
				t.Fatalf("client_id query = %q", got)
			}
			if got := r.URL.Query().Get("app-name"); got != appName {
				t.Fatalf("app-name query = %q", got)
			}
			if got := r.URL.Query().Get("app-version"); got != appVersion {
				t.Fatalf("app-version query = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"all":"2026-05-15T12:00:00Z"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	activities, err := client.GetActivities("client-id", "token")
	if err != nil {
		t.Fatalf("GetActivities() error = %v", err)
	}
	if activities["all"] != "2026-05-15T12:00:00Z" {
		t.Fatalf("activities = %+v", activities)
	}
}

func TestGetInitialSyncItemsAcceptsArrayResponses(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/movies" {
				t.Fatalf("path = %q, want /sync/movies", r.URL.Path)
			}
			if got := r.URL.Query().Get("extended"); got != "full" {
				t.Fatalf("extended query = %q", got)
			}
			if got := r.URL.Query().Get("episode_watched_at"); got != "yes" {
				t.Fatalf("episode_watched_at query = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[{"movie":{"title":"Inception","ids":{"imdb":"tt1375666"}}}]`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	items, err := client.GetInitialSyncItems("client-id", "token", "movies")
	if err != nil {
		t.Fatalf("GetInitialSyncItems() error = %v", err)
	}
	if len(items.Movies) != 1 {
		t.Fatalf("movies len = %d, want 1", len(items.Movies))
	}
}

func TestGetListItemsFiltersByStatus(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/all-items/movies/plantowatch" {
				t.Fatalf("path = %q, want /sync/all-items/movies/plantowatch", r.URL.Path)
			}
			// tmdb/tvdb are returned as strings by the all-items endpoint.
			body := `{"movies":[
				{"status":"plantowatch","movie":{"title":"Dune","year":2021,"ids":{"imdb":"tt1160419","tmdb":"438631"}}},
				{"status":"completed","movie":{"title":"Inception","year":2010,"ids":{"tmdb":"27205"}}}
			]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	items, err := client.GetListItems("client-id", "token", "movies", "plantowatch")
	if err != nil {
		t.Fatalf("GetListItems() error = %v", err)
	}
	// Endpoint is already status-scoped, so only the matching item is returned.
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	got := items[0]
	if got.Title != "Dune" || got.Year != 2021 {
		t.Fatalf("item = %+v, want Dune/2021", got)
	}
	if got.MediaType != "movie" {
		t.Fatalf("mediaType = %q, want movie", got.MediaType)
	}
	if got.IDs.IMDB != "tt1160419" || got.IDs.TMDB != 438631 {
		t.Fatalf("ids = %+v (want imdb tt1160419, tmdb 438631 parsed from string)", got.IDs)
	}
	if got.Status != "plantowatch" {
		t.Fatalf("status = %q, want plantowatch", got.Status)
	}
}

func TestGetListItemsEmptyStatusReturnsAll(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/all-items/shows" {
				t.Fatalf("path = %q, want /sync/all-items/shows", r.URL.Path)
			}
			body := `{"shows":[
				{"status":"watching","show":{"title":"Severance","year":2022,"ids":{"tvdb":"371980"}}},
				{"status":"completed","show":{"title":"Andor","year":2022,"ids":{"tmdb":"83867"}}}
			]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	items, err := client.GetListItems("client-id", "token", "shows", "")
	if err != nil {
		t.Fatalf("GetListItems() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	for _, it := range items {
		if it.MediaType != "show" {
			t.Fatalf("mediaType = %q, want show", it.MediaType)
		}
	}
	if items[0].IDs.TVDB != 371980 {
		t.Fatalf("tvdb = %d, want 371980 parsed from string", items[0].IDs.TVDB)
	}
}

func TestGetListItemsRejectsBadInput(t *testing.T) {
	client := NewClient()
	if _, err := client.GetListItems("c", "t", "books", ""); err == nil {
		t.Fatal("expected error for invalid media bucket")
	}
	if _, err := client.GetListItems("c", "t", "movies", "favourite"); err == nil {
		t.Fatal("expected error for invalid status bucket")
	}
}

func TestNormalizeSimklStatus(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"all":           "",
		"PlanToWatch":   "plantowatch",
		"plan_to_watch": "plantowatch",
		"on_hold":       "hold",
		"Completed":     "completed",
	}
	for in, want := range cases {
		if got := normalizeSimklStatus(in); got != want {
			t.Fatalf("normalizeSimklStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetAllItemsSinceSendsDateFrom(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/all-items" {
				t.Fatalf("path = %q, want /sync/all-items", r.URL.Path)
			}
			if got := r.URL.Query().Get("date_from"); got != "2026-05-15T12:00:00Z" {
				t.Fatalf("date_from query = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"movies":[],"shows":[],"anime":[]}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	if _, err := client.GetAllItemsSince("client-id", "token", "2026-05-15T12:00:00Z"); err != nil {
		t.Fatalf("GetAllItemsSince() error = %v", err)
	}
}

func TestBuildScrobbleRequestEpisodeUsesShowAndEpisode(t *testing.T) {
	req := BuildScrobbleRequest(testEpisodeUpdate(), 45.123)
	if req.Progress != 45.12 {
		t.Fatalf("progress = %v, want 45.12", req.Progress)
	}
	if req.Show == nil || req.Show.IDs.TVDB != 153021 || req.Show.IDs.IMDB != "tt1520211" {
		t.Fatalf("show ids = %+v", req.Show)
	}
	if req.Episode == nil || req.Episode.Season != 1 || req.Episode.Number != 3 {
		t.Fatalf("episode = %+v", req.Episode)
	}
}

func TestUndoShowsWithAllEpisodesNotFound_RemovesShow(t *testing.T) {
	var removeCalled bool
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/sync/history/remove" {
				removeCalled = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"deleted":{"shows":1}}`)),
					Header:     make(http.Header),
				}, nil
			}
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, nil
		}),
	})

	requested := []SyncHistoryShow{{
		IDs: IDs{TMDB: 873, TVDB: 70369, IMDB: "tt1466074"},
		Seasons: []SyncHistorySeason{{
			Number:   10,
			Episodes: []SyncHistoryEpisode{{Number: 14}},
		}},
	}}
	resp := &SyncHistoryResponse{}
	resp.Added.Episodes = 61
	resp.NotFound.Episodes = []struct {
		IDs     IDs `json:"ids"`
		Seasons []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number int `json:"number"`
			} `json:"episodes"`
		} `json:"seasons"`
	}{{
		IDs: IDs{TMDB: 873, TVDB: 70369, IMDB: "tt1466074"},
		Seasons: []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number int `json:"number"`
			} `json:"episodes"`
		}{{
			Number: 10,
			Episodes: []struct {
				Number int `json:"number"`
			}{{Number: 14}},
		}},
	}}

	undone := client.UndoShowsWithAllEpisodesNotFound("cid", "tok", requested, resp)
	if undone != 1 {
		t.Fatalf("undone = %d, want 1", undone)
	}
	if !removeCalled {
		t.Fatal("expected history/remove to be called")
	}
}

func TestUndoShowsWithAllEpisodesNotFound_PartialMatchKeepsShow(t *testing.T) {
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("should not remove on partial not_found, path=%s", req.URL.Path)
			return nil, nil
		}),
	})

	// Sent two episodes; only one not_found.
	requested := []SyncHistoryShow{{
		IDs: IDs{TMDB: 1406},
		Seasons: []SyncHistorySeason{{
			Number: 1,
			Episodes: []SyncHistoryEpisode{
				{Number: 1},
				{Number: 2},
			},
		}},
	}}
	resp := &SyncHistoryResponse{}
	resp.Added.Episodes = 1
	resp.NotFound.Episodes = []struct {
		IDs     IDs `json:"ids"`
		Seasons []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number int `json:"number"`
			} `json:"episodes"`
		} `json:"seasons"`
	}{{
		IDs: IDs{TMDB: 1406},
		Seasons: []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number int `json:"number"`
			} `json:"episodes"`
		}{{
			Number: 1,
			Episodes: []struct {
				Number int `json:"number"`
			}{{Number: 2}},
		}},
	}}

	if undone := client.UndoShowsWithAllEpisodesNotFound("cid", "tok", requested, resp); undone != 0 {
		t.Fatalf("undone = %d, want 0", undone)
	}
}

func TestSyncHistorySafe_UndoesOnNotFound(t *testing.T) {
	var paths []string
	client := NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			paths = append(paths, req.URL.Path)
			switch req.URL.Path {
			case "/sync/history":
				body := `{
					"added":{"movies":0,"shows":1,"episodes":61,"statuses":[]},
					"not_found":{"movies":[],"shows":[],"episodes":[{
						"ids":{"tmdb":873,"tvdb":70369,"imdb":"tt1466074"},
						"seasons":[{"number":10,"episodes":[{"number":14}]}]
					}]}
				}`
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			case "/sync/history/remove":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"deleted":{"shows":1}}`)),
					Header:     make(http.Header),
				}, nil
			default:
				t.Fatalf("unexpected path %s", req.URL.Path)
				return nil, nil
			}
		}),
	})

	req := SyncHistoryRequest{
		Shows: []SyncHistoryShow{{
			IDs: IDs{TMDB: 873, TVDB: 70369, IMDB: "tt1466074"},
			Seasons: []SyncHistorySeason{{
				Number:   10,
				Episodes: []SyncHistoryEpisode{{Number: 14, WatchedAt: "2023-11-21T16:34:05Z"}},
			}},
		}},
	}
	if _, err := client.SyncHistorySafe("cid", "tok", req); err != nil {
		t.Fatalf("SyncHistorySafe error: %v", err)
	}
	if len(paths) != 2 || paths[0] != "/sync/history" || paths[1] != "/sync/history/remove" {
		t.Fatalf("paths = %v, want [/sync/history /sync/history/remove]", paths)
	}
}
