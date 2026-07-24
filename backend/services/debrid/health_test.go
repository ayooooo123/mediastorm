package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"novastream/models"
	"sync"
	"testing"
)

func TestExtractInfoHashFromMagnet(t *testing.T) {
	tests := []struct {
		name     string
		magnet   string
		expected string
	}{
		{
			name:     "standard magnet with single hash",
			magnet:   "magnet:?xt=urn:btih:ABCDEF1234567890&dn=Example",
			expected: "abcdef1234567890",
		},
		{
			name:     "magnet with uppercase hash",
			magnet:   "magnet:?xt=urn:btih:FEDCBA0987654321&tr=http://tracker.example.com",
			expected: "fedcba0987654321",
		},
		{
			name:     "magnet without additional parameters",
			magnet:   "magnet:?xt=urn:btih:1234567890ABCDEF",
			expected: "1234567890abcdef",
		},
		{
			name:     "invalid magnet without btih",
			magnet:   "magnet:?xt=urn:sha1:ABCDEF",
			expected: "",
		},
		{
			name:     "empty string",
			magnet:   "",
			expected: "",
		},
		{
			name:     "magnet with spaces in hash (trimmed)",
			magnet:   "magnet:?xt=urn:btih:  ABC123  &dn=test",
			expected: "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractInfoHashFromMagnet(tt.magnet)
			if result != tt.expected {
				t.Errorf("extractInfoHashFromMagnet(%q) = %q, want %q", tt.magnet, result, tt.expected)
			}
		})
	}
}

func TestShouldUseQuickTorboxCacheCheck(t *testing.T) {
	torbox := config.DebridProviderSettings{
		Name:     "Torbox",
		Provider: "torbox",
		APIKey:   "tb-key",
		Enabled:  true,
	}
	realDebrid := config.DebridProviderSettings{
		Name:     "Real-Debrid",
		Provider: "realdebrid",
		APIKey:   "rd-key",
		Enabled:  true,
	}

	tests := []struct {
		name              string
		providers         []config.DebridProviderSettings
		selected          *config.DebridProviderSettings
		requestedProvider string
		infoHash          string
		want              bool
	}{
		{
			name:      "single enabled torbox provider with hash",
			providers: []config.DebridProviderSettings{torbox},
			selected:  &torbox,
			infoHash:  "abcdef1234567890",
			want:      true,
		},
		{
			name:      "missing hash cannot use quick check",
			providers: []config.DebridProviderSettings{torbox},
			selected:  &torbox,
			want:      false,
		},
		{
			name:      "multiple enabled providers use full verification",
			providers: []config.DebridProviderSettings{torbox, realDebrid},
			selected:  &torbox,
			infoHash:  "abcdef1234567890",
			want:      false,
		},
		{
			name:              "explicit non torbox provider uses full verification",
			providers:         []config.DebridProviderSettings{torbox},
			selected:          &torbox,
			requestedProvider: "realdebrid",
			infoHash:          "abcdef1234567890",
			want:              false,
		},
		{
			name:      "selected non torbox provider uses full verification",
			providers: []config.DebridProviderSettings{realDebrid},
			selected:  &realDebrid,
			infoHash:  "abcdef1234567890",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUseQuickTorboxCacheCheck(tt.providers, tt.selected, tt.requestedProvider, tt.infoHash)
			if got != tt.want {
				t.Fatalf("shouldUseQuickTorboxCacheCheck() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCheckQuickCacheOnlySkipsWhenNoSafeInstantPath(t *testing.T) {
	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{
			Name:     "Real-Debrid",
			Provider: "realdebrid",
			APIKey:   "rd-key",
			Enabled:  true,
		},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	hs := NewHealthService(cfg)
	health, err := hs.CheckQuickCacheOnly(context.Background(), models.NZBResult{
		Title:       "Example",
		Link:        "magnet:?xt=urn:btih:abcdef1234567890",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"infoHash": "abcdef1234567890",
		},
	})
	if err != nil {
		t.Fatalf("CheckQuickCacheOnly returned error: %v", err)
	}
	if health.Status != "skipped" || health.Cached || health.Healthy {
		t.Fatalf("expected skipped quick-only health, got %#v", health)
	}
}

func TestQuickCacheDedupKeyUsesProviderAndInfoHash(t *testing.T) {
	result := models.NZBResult{
		Link: "magnet:?xt=urn:btih:ABCDEF1234567890&dn=Example",
		Attributes: map[string]string{
			"provider": "TorBox",
		},
	}
	if got, want := quickCacheDedupKey(result), "torbox:abcdef1234567890"; got != want {
		t.Fatalf("quickCacheDedupKey() = %q, want %q", got, want)
	}

	result.Attributes["infoHash"] = "FEDCBA0987654321"
	if got, want := quickCacheDedupKey(result), "torbox:fedcba0987654321"; got != want {
		t.Fatalf("quickCacheDedupKey() with attribute hash = %q, want %q", got, want)
	}
}

func TestActiveTorrentTracking(t *testing.T) {
	hs := NewHealthService(nil)

	// Initially not active
	if hs.isTorrentActive("torbox", "123") {
		t.Fatal("torrent should not be active initially")
	}

	// Mark active
	hs.MarkTorrentActive("torbox", "123")
	if !hs.isTorrentActive("torbox", "123") {
		t.Fatal("torrent should be active after MarkTorrentActive")
	}

	// Different provider should not be active
	if hs.isTorrentActive("realdebrid", "123") {
		t.Fatal("different provider should not be active")
	}

	// Mark inactive
	hs.MarkTorrentInactive("torbox", "123")
	if hs.isTorrentActive("torbox", "123") {
		t.Fatal("torrent should not be active after MarkTorrentInactive")
	}
}

func TestActiveTorrentConcurrency(t *testing.T) {
	hs := NewHealthService(nil)

	var wg sync.WaitGroup
	// Simulate concurrent mark/check from health + playback goroutines
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			hs.MarkTorrentActive("torbox", "456")
			hs.isTorrentActive("torbox", "456")
		}()
		go func() {
			defer wg.Done()
			hs.isTorrentActive("torbox", "456")
			hs.MarkTorrentInactive("torbox", "456")
		}()
	}
	wg.Wait()
}

func TestPreResolvedPositiveHealthCacheUsesReleaseIdentity(t *testing.T) {
	hs := NewHealthService(nil)
	first := models.NZBResult{
		Title: "Game.of.Thrones.S06E10.The.Winds.of.Winter.2160p.BluRay.REMUX.HEVC-PB69.mkv",
		Attributes: map[string]string{
			"scraper":   "aiostreams",
			"tracker":   "ElfCache",
			"raw_title": "Game.of.Thrones.S06E10.The.Winds.of.Winter.2160p.BluRay.REMUX.HEVC-PB69.mkv",
		},
	}
	second := first
	second.Link = "https://aiostreams.example/playback/new-signed-url"

	key := preResolvedHealthCacheKey(first)
	hs.rememberPreResolvedHealth(key, &DebridHealthCheck{
		Healthy: true,
		Status:  "cached",
		Cached:  true,
		AudioTracks: []AudioTrackInfo{{
			Index:    4,
			Language: "eng",
			Codec:    "truehd",
		}},
	})

	cached, ok := hs.cachedPreResolvedHealth(preResolvedHealthCacheKey(second))
	if !ok {
		t.Fatal("expected cached health for the same pre-resolved release identity")
	}
	if !cached.Healthy || !cached.Cached || cached.Status != "cached" {
		t.Fatalf("cached health = %#v, want healthy cached", cached)
	}
	if len(cached.AudioTracks) != 1 || cached.AudioTracks[0].Language != "eng" {
		t.Fatalf("cached audio tracks = %#v", cached.AudioTracks)
	}
}

func TestPreResolvedInternetArchiveHead500FallsBackToRangeGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			http.Error(w, "archive edge error", http.StatusInternalServerError)
		case http.MethodGet:
			if got := r.Header.Get("Range"); got != "bytes=0-1023" {
				t.Fatalf("Range header = %q, want bytes=0-1023", got)
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Range", "bytes 0-1023/118544272")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(make([]byte, 1024))
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer server.Close()

	hs := NewHealthService(config.NewManager(t.TempDir() + "/settings.json"))
	health, err := hs.CheckHealth(context.Background(), models.NZBResult{
		Title:       "Dragnet/Season 1/Dragnet (1951) - S01E01 - The Human Bomb.mp4",
		Link:        server.URL + "/video.mp4",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"preresolved": "true",
			"stream_url":  server.URL + "/video.mp4",
			"scraper":     "internetarchive",
			"tracker":     "archive.org",
		},
	}, false)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if !health.Healthy || !health.Cached {
		t.Fatalf("expected healthy cached stream, got %#v", health)
	}
}

func TestPreResolvedNonArchiveHead500IsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		http.Error(w, "provider error", http.StatusInternalServerError)
	}))
	defer server.Close()

	hs := NewHealthService(config.NewManager(t.TempDir() + "/settings.json"))
	health, err := hs.CheckHealth(context.Background(), models.NZBResult{
		Title:       "Provider stream",
		Link:        server.URL + "/video.mp4",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"preresolved": "true",
			"stream_url":  server.URL + "/video.mp4",
			"scraper":     "other",
		},
	}, false)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if health.Healthy || health.Cached || health.ErrorMessage != "stream returned HTTP 500" {
		t.Fatalf("expected non-archive stream to be rejected, got %#v", health)
	}
}

func TestPreResolvedHead405RejectsElfHostedSlatePlaylist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			if got := r.Header.Get("Range"); got != "bytes=0-4095" {
				t.Fatalf("Range header = %q, want bytes=0-4095", got)
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:120.960,\nhttps://slate.elfhosted.com/cache/test/seg.ts\n#EXT-X-ENDLIST\n"))
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer server.Close()

	hs := NewHealthService(config.NewManager(t.TempDir() + "/settings.json"))
	hs.SetFFProbePath("/path/that/must/not/run")
	health, err := hs.CheckHealth(context.Background(), models.NZBResult{
		Title:       "Uncached Comet stream",
		Link:        server.URL + "/playback/test",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"preresolved": "true",
			"stream_url":  server.URL + "/playback/test",
			"scraper":     "comet",
			"tracker":     "Comet",
		},
	}, false)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if health.Healthy || health.Cached || health.Status != "not_cached" {
		t.Fatalf("expected slate stream to be rejected, got %#v", health)
	}
	if health.ErrorMessage != "stream redirected to an unavailable-content placeholder" {
		t.Fatalf("unexpected error message: %q", health.ErrorMessage)
	}
}
