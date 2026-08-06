package plex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestOpenServerPathRejectsEmptyPath(t *testing.T) {
	client := NewClient("test-client")
	server := PlexResource{
		AccessToken: "tok",
		Connections: []PlexConnection{{Protocol: "https", URI: "https://plex.example:32400"}},
	}
	_, err := client.OpenServerPath(context.Background(), server, "", http.MethodGet, "bytes=0-1")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error=%q, want mention of empty path", err)
	}
}

func TestOpenServerPathRejectsServerRoot(t *testing.T) {
	client := NewClient("test-client")
	server := PlexResource{
		AccessToken: "tok",
		Connections: []PlexConnection{{Protocol: "https", URI: "https://plex.example:32400"}},
	}
	// Whitespace-only collapses to empty after TrimSpace.
	_, err := client.OpenServerPath(context.Background(), server, "   ", http.MethodGet, "")
	if err == nil {
		t.Fatal("expected error for blank path")
	}
}

func TestGetServerMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/metadata/264995" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"264995","title":"Test","Media":[{"Part":[{"id":7,"key":"/library/parts/7/x/file.mp4","file":"/f.mp4","size":10}]}]}]}}`))
	}))
	defer server.Close()

	client := NewClient("test-client")
	resource := PlexResource{
		AccessToken: "tok",
		Connections: []PlexConnection{{Protocol: "http", URI: server.URL, Local: true}},
	}
	item, err := client.GetServerMetadata(context.Background(), resource, "264995")
	if err != nil {
		t.Fatalf("GetServerMetadata: %v", err)
	}
	if item.RatingKey != "264995" || len(item.Media) != 1 || len(item.Media[0].Part) != 1 {
		t.Fatalf("unexpected item: %#v", item)
	}
	if item.Media[0].Part[0].Key != "/library/parts/7/x/file.mp4" {
		t.Fatalf("part key=%q", item.Media[0].Part[0].Key)
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

func TestNormalizeServerURL(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{" https://plex.example.test:32400/ ", "https://plex.example.test:32400"},
		{"http://100.64.0.10:32400/plex/", "http://100.64.0.10:32400/plex"},
	} {
		got, err := NormalizeServerURL(tc.input)
		if err != nil {
			t.Fatalf("NormalizeServerURL(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeServerURL(%q)=%q, want %q", tc.input, got, tc.want)
		}
	}

	for _, input := range []string{
		"plex.example.test:32400",
		"ftp://plex.example.test",
		"http://user:pass@plex.example.test",
		"https://plex.example.test?token=secret",
	} {
		if _, err := NormalizeServerURL(input); err == nil {
			t.Fatalf("NormalizeServerURL(%q) unexpectedly succeeded", input)
		}
	}
}

func TestGetServerLibrariesAtUsesSelectedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("X-Plex-Token"); got != "server-token" {
			t.Fatalf("X-Plex-Token=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[
			{"key":"1","title":"Movies","type":"movie"},
			{"key":"2","title":"Shows","type":"show"},
			{"key":"3","title":"Music","type":"artist"}
		]}}`))
	}))
	defer server.Close()

	client := NewClient("strmr-test")
	resource := PlexResource{
		Name:        "Remote PMS",
		AccessToken: "server-token",
		Connections: []PlexConnection{{Protocol: "http", URI: "http://192.0.2.1:32400", Local: true}},
	}
	libraries, err := client.GetServerLibrariesAt(context.Background(), resource, server.URL)
	if err != nil {
		t.Fatalf("GetServerLibrariesAt() error = %v", err)
	}
	if len(libraries) != 3 || libraries[0].Title != "Movies" || libraries[1].Title != "Shows" || libraries[2].Title != "Music" {
		t.Fatalf("libraries=%#v", libraries)
	}
}
