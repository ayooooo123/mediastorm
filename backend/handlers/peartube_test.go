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
	"novastream/internal/streamheaders"
	"novastream/models"
	"novastream/services/peartube"
	"novastream/services/streaming"
)

// archiveRelay is a /v1 relay that records acquisitions and cancellations.
type archiveRelay struct {
	mu        sync.Mutex
	local     map[string]bool
	active    map[string]bool
	failed    map[string]string
	cancelled []string
	acquired  []map[string]any
	streamURL string
}

func (a *archiveRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/search":
		id := r.URL.Query().Get("id")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": id, "local": a.local[id], "streamUrl": a.streamURL}}})
	case "/v1/jobs":
		jobs := []map[string]any{}
		for id := range a.active {
			jobs = append(jobs, map[string]any{"jobId": "j-" + id, "id": id, "status": "announcing"})
		}
		for id, jobID := range a.failed {
			jobs = append(jobs, map[string]any{"jobId": jobID, "id": id, "status": "failed"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
	case "/v1/acquire":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.acquired = append(a.acquired, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"jobId": "new", "id": body["id"], "status": "queued"})
	default:
		if jobID, ok := strings.CutPrefix(r.URL.Path, "/v1/jobs/"); ok && r.Method == http.MethodDelete {
			a.cancelled = append(a.cancelled, jobID)
			for id, fid := range a.failed {
				if fid == jobID {
					delete(a.failed, id)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobId": jobID, "status": "removed"})
			return
		}
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

func (s *archiveStreams) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	s.mu.Lock()
	s.streamed = append(s.streamed, req.Path+"|"+req.RangeHeader)
	s.mu.Unlock()
	if req.RangeHeader == "bytes=2-" {
		h := http.Header{"Content-Range": []string{"bytes 2-4/5"}}
		return &streaming.Response{Body: io.NopCloser(strings.NewReader("dia")), Status: http.StatusPartialContent, ContentLength: 3, Headers: h}, nil
	}
	h := http.Header{}
	h.Set("ETag", `"e1"`)
	return &streaming.Response{Body: io.NopCloser(strings.NewReader("media")), Status: http.StatusOK, ContentLength: 5, Headers: h}, nil
}

func TestPearTubeArchivesPlayedTitleOnce(t *testing.T) {
	relay := &archiveRelay{
		local:  map[string]bool{"imdb:tt0133093": true},
		active: map[string]bool{"imdb:tt0068646": true},
		failed: map[string]string{"imdb:tt0944947:s01e03": "dead-job-1"},
	}
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
	handler.OnPlaybackStarted(episode) // a heartbeat for the same title and path
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
	// Debrid sources now route through MediaStorm's SourceRoute rather than leaking IP-bound CDN URLs.
	debridSource := acquired[0]["source"].(map[string]any)
	if debridSource["url"] != "http://mediastorm.local"+peartube.SourceRoute {
		t.Fatalf("debrid source = %v, want SourceRoute", debridSource)
	}
	debridAuth := debridSource["headers"].(map[string]any)["Authorization"].(string)
	if !strings.HasPrefix(debridAuth, "Bearer ") {
		t.Fatalf("debrid auth = %q", debridAuth)
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

	// A migrated playback whose earlier relay job failed cancels the corpse and submits the working source.
	if err := handler.archiveTitle(ctx, client, "http://mediastorm.local", "imdb:tt0944947:s01e03", "Show.S01E03.1080p.mkv", "/debrid/realdebrid/999/Show.S01E03.mkv"); err != nil {
		t.Fatalf("archiveTitle(migrated): %v", err)
	}
	if len(relay.cancelled) != 1 || relay.cancelled[0] != "dead-job-1" {
		t.Fatalf("cancelled = %v, want [dead-job-1]", relay.cancelled)
	}

	// Usenet and debrid tokens survive mid-stream Range reconnects within TTL while rejecting bad tokens.
	if err := handler.archiveTitle(ctx, client, "http://mediastorm.local", "imdb:tt0111161", "t", "/webdav/nzbs/Movie.mkv"); err != nil {
		t.Fatalf("archiveTitle(usenet): %v", err)
	}
	acquired = relay.acquisitions()
	source := acquired[len(acquired)-1]["source"].(map[string]any)
	if source["url"] != "http://mediastorm.local"+peartube.SourceRoute {
		t.Fatalf("usenet source url = %v", source["url"])
	}
	authorization := source["headers"].(map[string]any)["Authorization"].(string)
	fetch := func(authHeader, rangeHeader string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, peartube.SourceRoute, nil)
		if authHeader != "" {
			request.Header.Set("Authorization", authHeader)
		}
		if rangeHeader != "" {
			request.Header.Set("Range", rangeHeader)
		}
		recorder := httptest.NewRecorder()
		handler.Sources().ServeHTTP(recorder, request)
		return recorder
	}
	if first := fetch(authorization, ""); first.Code != http.StatusOK || first.Body.String() != "media" || first.Header().Get("ETag") != `"e1"` {
		t.Fatalf("first source fetch = %d %q etag=%q", first.Code, first.Body.String(), first.Header().Get("ETag"))
	}
	if resumed := fetch(authorization, "bytes=2-"); resumed.Code != http.StatusPartialContent || resumed.Body.String() != "dia" {
		t.Fatalf("resumed Range fetch = %d %q, want 206 dia", resumed.Code, resumed.Body.String())
	}
	if bad := fetch("Bearer wrong", ""); bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad token fetch = %d, want 401", bad.Code)
	}
	if len(streams.streamed) != 2 || streams.streamed[0] != "/nzbs/Movie.mkv|" || streams.streamed[1] != "/nzbs/Movie.mkv|bytes=2-" {
		t.Fatalf("streamed paths = %v", streams.streamed)
	}
}

// MediaStorm and the relay often share a host. Only the exact stream origin a
// relay search returned is a relay stream; another port on that host is an
// ordinary source and is archived.
func TestPearTubeArchivesSourceOnRelayHostButNotRelayStreams(t *testing.T) {
	relay := &archiveRelay{local: map[string]bool{}, active: map[string]bool{}}
	server := httptest.NewServer(relay)
	defer server.Close()
	relay.streamURL = server.URL + "/stream?key=x"
	var gotReferer, gotUA, gotIfRange string
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		gotUA = r.Header.Get("User-Agent")
		gotIfRange = r.Header.Get("If-Range")
		_, _ = io.WriteString(w, "media")
	}))
	defer direct.Close()

	handler := NewPearTubeHandler(&archiveStreams{})
	videoHandler := NewVideoHandler(false, "", "")
	vhSettings := config.DefaultSettings()
	vhSettings.Server.AllowedPrivateMediaOrigins = []string{direct.URL}
	videoHandler.SetConfigManager(staticVideoConfigProvider{settings: vhSettings})
	handler.SetVideoHandler(videoHandler)
	settings := config.Settings{TorrentScrapers: []config.TorrentScraperConfig{{
		Type: config.TorrentScraperTypePearTube, URL: server.URL, APIKey: "secret", Enabled: true,
		Config: map[string]string{config.PearTubeConfigArchiveEnabled: "true"},
	}}}
	settings.Server.ExternalBackendURL = "http://mediastorm.local"
	handler.ApplyPearTubeSettings(settings)
	client, err := peartube.New(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), "imdb:tt0110912"); err != nil {
		t.Fatal(err)
	}

	handler.OnPlaybackStarted(models.PlaybackProgressUpdate{MediaType: "movie", ExternalIDs: map[string]string{"imdb": "tt0110912"}, SourcePath: relay.streamURL})
	handler.mu.Lock()
	_, claimed := handler.claims["imdb:tt0110912"]
	handler.mu.Unlock()
	if claimed {
		t.Fatal("a relay stream was treated as an archivable source")
	}

	source := streamheaders.Attach(direct.URL+"/movie.mkv", map[string]string{"Referer": "https://addon.example/watch"})
	handler.OnPlaybackStarted(models.PlaybackProgressUpdate{MediaType: "movie", ExternalIDs: map[string]string{"imdb": "tt0120737"}, SourcePath: source})
	deadline := time.Now().Add(2 * time.Second)
	for len(relay.acquisitions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	acquired := relay.acquisitions()
	if len(acquired) != 1 || acquired[0]["source"].(map[string]any)["url"] != "http://mediastorm.local"+peartube.SourceRoute {
		t.Fatalf("acquisitions = %v, want SourceRoute", acquired)
	}
	authHeader := acquired[0]["source"].(map[string]any)["headers"].(map[string]any)["Authorization"].(string)
	req := httptest.NewRequest(http.MethodGet, peartube.SourceRoute, nil)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Range", "bytes=0-4")
	req.Header.Set("If-Range", `"e1"`)
	rec := httptest.NewRecorder()
	handler.Sources().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "media" {
		t.Fatalf("proxied external source = %d %q", rec.Code, rec.Body.String())
	}
	if gotReferer != "https://addon.example/watch" || !strings.HasPrefix(gotUA, "VLC/") || gotIfRange != `"e1"` {
		t.Fatalf("upstream headers referer=%q ua=%q if-range=%q", gotReferer, gotUA, gotIfRange)
	}
}

// The relay fetches usenet sources from sourceUrl, a LAN address, not from
// the public external backend URL used for invite links.
func TestPearTubeUsenetSourceUsesSourceURL(t *testing.T) {
	relay := &archiveRelay{local: map[string]bool{}, active: map[string]bool{}}
	server := httptest.NewServer(relay)
	defer server.Close()
	handler := NewPearTubeHandler(&archiveStreams{})
	settings := config.Settings{TorrentScrapers: []config.TorrentScraperConfig{{
		Type: config.TorrentScraperTypePearTube, URL: server.URL, APIKey: "secret", Enabled: true,
		Config: map[string]string{config.PearTubeConfigArchiveEnabled: "true", config.PearTubeConfigSourceURL: "http://10.0.0.5:7777/"},
	}}}
	settings.Server.ExternalBackendURL = "https://public.example"
	handler.ApplyPearTubeSettings(settings)

	handler.OnPlaybackStarted(models.PlaybackProgressUpdate{MediaType: "movie", ExternalIDs: map[string]string{"imdb": "tt0000099"}, SourcePath: "/webdav/nzbs/Movie.mkv"})
	deadline := time.Now().Add(2 * time.Second)
	for len(relay.acquisitions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	acquired := relay.acquisitions()
	if len(acquired) != 1 || acquired[0]["source"].(map[string]any)["url"] != "http://10.0.0.5:7777"+peartube.SourceRoute {
		t.Fatalf("acquisitions = %v, want the sourceUrl base", acquired)
	}
}

type blockingBody struct {
	gate chan struct{}
	rest io.Reader
}

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.gate
	return b.rest.Read(p)
}

func (b *blockingBody) Close() error { return nil }

type blockingRangeStreams struct {
	gate chan struct{}
}

func (s *blockingRangeStreams) Stream(_ context.Context, _ streaming.Request) (*streaming.Response, error) {
	h := http.Header{"Content-Range": []string{"bytes 3630339-3630342/3840000"}}
	return &streaming.Response{
		Body:          &blockingBody{gate: s.gate, rest: strings.NewReader("tail")},
		Status:        http.StatusPartialContent,
		ContentLength: 4,
		Headers:       h,
	}, nil
}

// Usenet Range resumes wrap the segment in skipReader, which can chew through several MiB before emitting byte 1.
// SourceRoute must flush 206 + Content-Range onto the socket before Body.Read unblocks so the relay's header timer never trips.
func TestPearTubeSourceFlushesRangeHeadersBeforeSlowBodyRead(t *testing.T) {
	gate := make(chan struct{})
	var closeOnce sync.Once
	unblock := func() { closeOnce.Do(func() { close(gate) }) }
	sources := peartube.NewSources(&blockingRangeStreams{gate: gate})
	token, err := sources.Issue("/BgZbaqpxb0zg.mkv")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(sources)
	defer srv.Close()
	defer unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+peartube.SourceRoute, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Range", "bytes=3630339-")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Range request timed out waiting for un-flushed 206 headers: %v", err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("206 headers arrived after %v while body was still blocked", elapsed)
	}
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != "bytes 3630339-3630342/3840000" {
		t.Fatalf("resp = %d Content-Range=%q", resp.StatusCode, resp.Header.Get("Content-Range"))
	}
	unblock()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "tail" {
		t.Fatalf("body = %q, err = %v", string(body), err)
	}
}
