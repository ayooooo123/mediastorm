package plex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPlexLibraryItemAcceptsLegacyAndProviderGUIDs(t *testing.T) {
	var item PlexLibraryItem
	err := json.Unmarshal([]byte(`{
		"ratingKey":"10",
		"guid":"plex://movie/abc",
		"Guid":[{"id":"imdb://tt1234567"},{"id":"tmdb://42"}]
	}`), &item)
	if err != nil {
		t.Fatalf("unmarshal Plex item: %v", err)
	}
	if item.GUID != "plex://movie/abc" {
		t.Fatalf("GUID = %q", item.GUID)
	}
	if len(item.Guid) != 2 || item.Guid[0].ID != "imdb://tt1234567" || item.Guid[1].ID != "tmdb://42" {
		t.Fatalf("provider GUIDs = %#v", item.Guid)
	}
}

func TestReportTimeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/:/timeline" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("ratingKey"); got != "42" {
			t.Fatalf("ratingKey=%q", got)
		}
		if got := r.URL.Query().Get("state"); got != "playing" {
			t.Fatalf("state=%q", got)
		}
		if got := r.URL.Query().Get("time"); got != "12000" {
			t.Fatalf("time=%q", got)
		}
		if got := r.Header.Get("X-Plex-Session-Identifier"); got != "session-1" {
			t.Fatalf("session header=%q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient("strmr-test")
	resource := PlexResource{AccessToken: "token", Connections: []PlexConnection{{Protocol: "http", URI: server.URL, Local: true}}}
	if err := client.ReportTimeline(context.Background(), resource, "42", "session-1", "playing", 12*time.Second, 2*time.Hour); err != nil {
		t.Fatalf("ReportTimeline() error = %v", err)
	}
}
