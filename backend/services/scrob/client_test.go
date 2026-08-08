package scrob

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestGetHistoryPaginatesAndUsesAPIKey(t *testing.T) {
	var pages []string
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("X-Api-Key"); got != "secret" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		return jsonResponse(200, `{"page":`+page+`,"total_pages":2,"results":[{"id":`+page+`,"media":{"tmdb_id":`+page+`,"type":"movie"}}]}`), nil
	})}
	items, err := NewClientWithHTTPClient(httpClient).GetHistory(context.Background(), "https://scrob.example/", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("items=%v pages=%v", items, pages)
	}
}

func TestLoginAndAddHistory(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/proxy/auth/login":
			if got := r.Header.Get("X-Api-Key"); got != "api-key" {
				t.Fatalf("X-Api-Key = %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "password=pw&username=liam" {
				t.Fatalf("form = %q", body)
			}
			return jsonResponse(200, `{"access_token":"jwt","token_type":"bearer"}`), nil
		case "/api/proxy/history":
			if r.Header.Get("Authorization") != "Bearer jwt" {
				t.Fatal("missing bearer")
			}
			if r.Header.Get("X-Api-Key") != "api-key" {
				t.Fatal("missing API key")
			}
			var event WatchEvent
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Fatal(err)
			}
			if event.MediaType != "episode" || event.SeriesTMDBID != 42 || event.SeasonNumber != 1 {
				t.Fatalf("event=%+v", event)
			}
			return jsonResponse(200, `{"status":"ok"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})}
	client := NewClientWithHTTPClient(httpClient)
	token, err := client.Login(context.Background(), "https://scrob.example", "api-key", "liam", "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddHistory(context.Background(), "https://scrob.example", "api-key", token, WatchEvent{MediaType: "episode", SeriesTMDBID: 42, SeasonNumber: 1, EpisodeNumber: 2, Completed: true}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginCompletesTwoFactorFlow(t *testing.T) {
	client := NewClientWithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("X-Api-Key"); got != "api-key" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		if r.URL.Path == "/api/proxy/auth/login" {
			return jsonResponse(200, `{"requires_2fa":true,"temp_token":"pending"}`), nil
		}
		if r.URL.Path != "/api/proxy/auth/2fa/verify-login" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["temp_token"] != "pending" || payload["code"] != "123456" {
			t.Fatalf("payload=%v", payload)
		}
		return jsonResponse(200, `{"access_token":"2fa-jwt","token_type":"bearer"}`), nil
	})})
	token, err := client.Login(context.Background(), "https://scrob.example", "api-key", "liam", "pw", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if token != "2fa-jwt" {
		t.Fatalf("token=%q", token)
	}
}

func TestGenerateTOTPCodeRFCVector(t *testing.T) {
	// RFC 6238 SHA-1 vector at T=1 produces 94287082; the six-digit form is 287082.
	code, err := GenerateTOTPCode("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("code=%q", code)
	}
}

func TestHistoryEventAcceptsTimezoneLessScrobTimestamp(t *testing.T) {
	var event HistoryEvent
	if err := json.Unmarshal([]byte(`{"id":1,"watched_at":"2026-08-08T18:49:42.123456","completed":true,"media":{"type":"movie","tmdb_id":550}}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.WatchedAt == nil || event.WatchedAt.Location() != time.UTC || event.WatchedAt.Nanosecond() != 123456000 {
		t.Fatalf("watchedAt=%v", event.WatchedAt)
	}
}

func TestWatchEventIncludesSeasonZero(t *testing.T) {
	data, err := json.Marshal(WatchEvent{MediaType: "episode", SeriesTMDBID: 42, SeasonNumber: 0, EpisodeNumber: 3, Completed: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"season_number":0`) {
		t.Fatalf("payload omitted season zero: %s", data)
	}
}

func TestManualSessionLifecycle(t *testing.T) {
	step := 0
	client := NewClientWithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		step++
		if r.Header.Get("X-Api-Key") != "api-key" || r.Header.Get("Authorization") != "Bearer jwt" {
			t.Fatalf("missing Scrob authentication headers")
		}
		switch step {
		case 1:
			if r.Method != http.MethodPost || r.URL.Path != "/api/proxy/history/session/start" {
				t.Fatalf("start request = %s %s", r.Method, r.URL.Path)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["season_number"] != float64(0) || body["episode_number"] != float64(3) {
				t.Fatalf("start payload=%v", body)
			}
			return jsonResponse(200, `{"session_key":"manual-1-2","media_id":2,"runtime":24}`), nil
		case 2:
			if r.Method != http.MethodPatch || r.URL.Path != "/api/proxy/history/session/manual-1-2" {
				t.Fatalf("update request = %s %s", r.Method, r.URL.Path)
			}
			return jsonResponse(200, `{"status":"ok"}`), nil
		case 3:
			if r.Method != http.MethodDelete || r.URL.Path != "/api/proxy/history/session/manual-1-2" {
				t.Fatalf("stop request = %s %s", r.Method, r.URL.Path)
			}
			return jsonResponse(200, `{"status":"ok"}`), nil
		default:
			t.Fatalf("unexpected request %d", step)
			return nil, nil
		}
	})})

	season, episode := 0, 3
	started, err := client.StartSession(context.Background(), "https://scrob.example", "api-key", "jwt", ManualSessionStart{
		MediaType: "episode", ShowTMDBID: 42, SeasonNumber: &season, EpisodeNumber: &episode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateSession(context.Background(), "https://scrob.example", "api-key", "jwt", started.SessionKey, ManualSessionUpdate{ProgressSeconds: 120, State: "paused"}); err != nil {
		t.Fatal(err)
	}
	if err := client.StopSession(context.Background(), "https://scrob.example", "api-key", "jwt", started.SessionKey); err != nil {
		t.Fatal(err)
	}
}
