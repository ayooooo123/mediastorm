package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/peartube"
	"novastream/services/streaming"
)

// archiveRelay is a /v1 relay that records acquisitions.
type archiveRelay struct {
	mu       sync.Mutex
	local    map[string]bool
	active   map[string]bool
	acquired []map[string]any
}

func (a *archiveRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/search":
		id := r.URL.Query().Get("id")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": id, "local": a.local[id], "streamUrl": "http://relay/x"}}})
	case "/v1/jobs":
		jobs := []map[string]any{}
		for id := range a.active {
			jobs = append(jobs, map[string]any{"jobId": "j-" + id, "id": id, "status": "running"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
	case "/v1/acquire":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.acquired = append(a.acquired, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"jobId": "new", "id": body["id"], "status": "queued"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *archiveRelay) acquisitions() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any(nil), a.acquired...)
}

// archiveStreams resolves debrid paths to a CDN URL and streams anything else.
type archiveStreams struct {
	mu       sync.Mutex
	streamed []string
}

func (s *archiveStreams) GetDirectURL(_ context.Context, path string) (string, error) {
	if strings.HasPrefix(path, "/debrid/") {
		return "https://cdn.example" + path, nil
	}
	return "", streaming.ErrNotFound
}

func (s *archiveStreams) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	s.mu.Lock()
	s.streamed = append(s.streamed, req.Path)
	s.mu.Unlock()
	return &streaming.Response{Body: io.NopCloser(strings.NewReader("media")), Status: http.StatusOK, Headers: http.Header{}}, nil
}

func TestPearTubeArchivesPlayedTitleOnce(t *testing.T) {
	relay := &archiveRelay{local: map[string]bool{"imdb:tt0133093": true}, active: map[string]bool{"imdb:tt0068646": true}}
	server := httptest.NewServer(relay)
	defer server.Close()
	streams := &archiveStreams{}
	handler := NewPearTubeHandler(streams)
	settings := config.Settings{TorrentScrapers: []config.TorrentScraperConfig{{
		Type: config.TorrentScraperTypePearTube, URL: server.URL, APIKey: "secret", Enabled: true,
		Config: map[string]string{config.PearTubeConfigArchiveEnabled: "true"},
	}}}
	settings.Server.ExternalBackendURL = "http://mediastorm.local"
	handler.ApplyPearTubeSettings(settings)

	episode := models.PlaybackProgressUpdate{
		MediaType: "episode", SeasonNumber: 1, EpisodeNumber: 2,
		ExternalIDs:  map[string]string{"imdb": "tt0944947"},
		SourcePath:   "/debrid/realdebrid/123/Show.S01E02.mkv",
		ReleaseTitle: "Show.S01E02.1080p.mkv",
	}
	handler.OnPlaybackStarted(episode)
	handler.OnPlaybackStarted(episode) // a heartbeat for the same title
	deadline := time.Now().Add(2 * time.Second)
	for len(relay.acquisitions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	acquired := relay.acquisitions()
	if len(acquired) != 1 {
		t.Fatalf("acquisitions = %v, want exactly one", acquired)
	}
	if acquired[0]["id"] != "imdb:tt0944947:s01e02" || acquired[0]["title"] != "Show.S01E02.1080p.mkv" {
		t.Fatalf("acquire = %v", acquired[0])
	}
	if source := acquired[0]["source"].(map[string]any); source["url"] != "https://cdn.example/debrid/realdebrid/123/Show.S01E02.mkv" || source["headers"] != nil {
		t.Fatalf("debrid source = %v", source)
	}

	// A PearTube stream is never archived back to the relay.
	fromRelay := models.PlaybackProgressUpdate{MediaType: "movie", ExternalIDs: map[string]string{"imdb": "tt0110912"}, SourcePath: server.URL + "/?key=x"}
	handler.OnPlaybackStarted(fromRelay)
	handler.mu.Lock()
	_, claimed := handler.claims["imdb:tt0110912"]
	handler.mu.Unlock()
	if claimed {
		t.Fatal("a relay stream was treated as an archivable source")
	}

	client, err := peartube.New(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Held locally, or already being fetched: nothing to do.
	for _, id := range []string{"imdb:tt0133093", "imdb:tt0068646"} {
		if err := handler.archiveTitle(ctx, client, "http://mediastorm.local", id, "t", "/debrid/x/1/f.mkv"); err != nil {
			t.Fatalf("archiveTitle(%s): %v", id, err)
		}
	}
	if n := len(relay.acquisitions()); n != 1 {
		t.Fatalf("acquisitions = %d after held/active titles, want 1", n)
	}

	// A usenet release only MediaStorm can read goes through a one-time URL.
	if err := handler.archiveTitle(ctx, client, "http://mediastorm.local", "imdb:tt0111161", "t", "/webdav/nzbs/Movie.mkv"); err != nil {
		t.Fatalf("archiveTitle(usenet): %v", err)
	}
	acquired = relay.acquisitions()
	source := acquired[len(acquired)-1]["source"].(map[string]any)
	if source["url"] != "http://mediastorm.local"+peartube.SourceRoute {
		t.Fatalf("usenet source url = %v", source["url"])
	}
	authorization := source["headers"].(map[string]any)["Authorization"].(string)
	fetch := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, peartube.SourceRoute, nil)
		request.Header.Set("Authorization", authorization)
		recorder := httptest.NewRecorder()
		handler.Sources().ServeHTTP(recorder, request)
		return recorder
	}
	if first := fetch(); first.Code != http.StatusOK || first.Body.String() != "media" {
		t.Fatalf("first source fetch = %d %q", first.Code, first.Body.String())
	}
	if len(streams.streamed) != 1 || streams.streamed[0] != "/nzbs/Movie.mkv" {
		t.Fatalf("streamed paths = %v", streams.streamed)
	}
	if second := fetch(); second.Code != http.StatusUnauthorized {
		t.Fatalf("second source fetch = %d, want 401 (one-time token)", second.Code)
	}
}
