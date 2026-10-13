package debrid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"novastream/models"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

type reusableFullProber struct {
	result *models.VideoFullResult
	calls  int
}

func (p *reusableFullProber) ProbeVideoFull(context.Context, string) (*models.VideoFullResult, error) {
	p.calls++
	return p.result, nil
}

func TestPreResolvedPlaybackReturnsReusableFullProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Length", "10485760")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	probe := &models.VideoFullResult{
		VideoCodec: "hevc",
		AudioStreams: []models.AudioStreamInfo{
			{Index: 1, Codec: "ac3", Language: "eng"},
		},
		SubtitleStreams: []models.SubtitleStreamInfo{
			{Index: 2, Codec: "hdmv_pgs_subtitle", Language: "eng"},
		},
	}
	prober := &reusableFullProber{result: probe}
	cfg := config.NewManager(t.TempDir() + "/settings.json")
	hs := NewHealthService(cfg)
	hs.SetFFProbePath("/path/that/must/not/run")
	hs.SetFullProber(prober)
	resolution, err := NewPlaybackService(cfg, hs).Resolve(context.Background(), models.NZBResult{
		Title:       "Bilby.2018.2160p.UHD.BluRay.Remux-PmP.mkv",
		Link:        server.URL + "/video.mkv",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"preresolved": "true",
			"stream_url":  server.URL + "/video.mkv",
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolution.Probe == nil {
		t.Fatalf("expected reusable probe, got %#v", resolution)
	}
	if resolution.Probe.VideoCodec != "hevc" || len(resolution.Probe.AudioStreams) != 1 {
		t.Fatalf("reusable probe metadata = %#v", resolution.Probe)
	}
	if prober.calls != 1 {
		t.Fatalf("ProbeVideoFull calls = %d, want 1", prober.calls)
	}
}

func TestPreResolvedHealthUsesSingleProbeForValidationAndTracks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Length", "10485760")
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	counterPath := t.TempDir() + "/ffprobe-calls"
	ffprobePath := t.TempDir() + "/ffprobe"
	ffprobe := `#!/bin/sh
printf x >> "$COUNTER_FILE"
printf '%s' '{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc"},{"index":1,"codec_type":"audio","codec_name":"ac3","tags":{"language":"eng"}},{"index":2,"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle","tags":{"language":"eng"}}]}'
`
	if err := os.WriteFile(ffprobePath, []byte(ffprobe), 0o755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
	t.Setenv("COUNTER_FILE", counterPath)

	hs := NewHealthService(config.NewManager(t.TempDir() + "/settings.json"))
	hs.SetFFProbePath(ffprobePath)
	health, err := hs.CheckHealth(context.Background(), models.NZBResult{
		Title:       "Bilby.2018.2160p.UHD.BluRay.Remux-PmP.mkv",
		Link:        server.URL + "/video.mkv",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"preresolved": "true",
			"stream_url":  server.URL + "/video.mkv",
			"scraper":     "comet",
		},
	}, false)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if !health.Healthy || !health.Cached {
		t.Fatalf("expected healthy cached stream, got %#v", health)
	}
	if len(health.AudioTracks) != 1 || health.AudioTracks[0].Language != "eng" {
		t.Fatalf("audio tracks = %#v", health.AudioTracks)
	}
	if len(health.SubtitleTracks) != 1 || !health.SubtitleTracks[0].IsBitmap {
		t.Fatalf("subtitle tracks = %#v", health.SubtitleTracks)
	}
	calls, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read ffprobe counter: %v", err)
	}
	if got := len(calls); got != 1 {
		t.Fatalf("ffprobe calls = %d, want 1", got)
	}
}

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
			name:      "torbox quick check remains available with multiple enabled providers",
			providers: []config.DebridProviderSettings{torbox, realDebrid},
			selected:  &torbox,
			infoHash:  "abcdef1234567890",
			want:      true,
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
			Name:     "Unsupported",
			Provider: "health_no_instant_path",
			APIKey:   "test-key",
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

func TestCheckQuickCacheOnlyTreatsAnyProviderCacheAsPlayable(t *testing.T) {
	uncachedProvider := &mockProvider{name: "health_union_uncached", instantCachedSet: true, instantCached: false}
	cachedProvider := &mockProvider{name: "health_union_cached", instantCachedSet: true, instantCached: true}
	RegisterProvider(uncachedProvider.name, func(string) Provider { return uncachedProvider })
	RegisterProvider(cachedProvider.name, func(string) Provider { return cachedProvider })

	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Name: "Uncached", Provider: uncachedProvider.name, APIKey: "one", Enabled: true},
		{Name: "Cached", Provider: cachedProvider.name, APIKey: "two", Enabled: true},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	health, err := NewHealthService(cfg).CheckQuickCacheOnly(context.Background(), models.NZBResult{
		Title: "Primal.S02E02",
		Link:  "magnet:?xt=urn:btih:abcdef1234567890",
		Attributes: map[string]string{
			"infoHash": "abcdef1234567890",
		},
	})
	if err != nil {
		t.Fatalf("CheckQuickCacheOnly returned error: %v", err)
	}
	if !health.Cached || !health.Healthy || health.Status != "cached" || health.Provider != cachedProvider.name {
		t.Fatalf("expected cached union result from %s, got %#v", cachedProvider.name, health)
	}
	if got := atomic.LoadInt64(&uncachedProvider.instantCalls); got != 1 {
		t.Fatalf("uncached provider calls = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&cachedProvider.instantCalls); got != 1 {
		t.Fatalf("cached provider calls = %d, want 1", got)
	}
}

func TestFullHealthUsesAuthoritativeUncachedAddResponse(t *testing.T) {
	provider := &mockProvider{
		name:             "health_known_uncached_add",
		cacheStatusKnown: true,
		cachedOnAdd:      false,
	}
	RegisterProvider(provider.name, func(string) Provider { return provider })

	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Name: "Known uncached", Provider: provider.name, APIKey: "test-key", Enabled: true},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	health, err := NewHealthService(cfg).CheckHealth(context.Background(), models.NZBResult{
		Title: "Example",
		Link:  "magnet:?xt=urn:btih:abcdef1234567890",
		Attributes: map[string]string{
			"infoHash": "abcdef1234567890",
		},
	}, true)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if health.Status != "not_cached" || health.Cached || health.Healthy {
		t.Fatalf("expected authoritative not-cached result, got %#v", health)
	}
	if got := atomic.LoadInt64(&provider.getInfoCalls); got != 0 {
		t.Fatalf("GetTorrentInfo calls = %d, want 0", got)
	}
	if got := atomic.LoadInt64(&provider.deleteCalls); got != 1 {
		t.Fatalf("DeleteTorrent calls = %d, want 1", got)
	}
}

func TestFullHealthPreservesAuthoritativeCachedStateWhenTrackPreparationFails(t *testing.T) {
	provider := &mockProvider{
		name:             "health_known_cached_add",
		cacheStatusKnown: true,
		cachedOnAdd:      true,
		status:           "queued",
	}
	RegisterProvider(provider.name, func(string) Provider { return provider })

	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Name: "Known cached", Provider: provider.name, APIKey: "test-key", Enabled: true},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	health, err := NewHealthService(cfg).CheckHealth(context.Background(), models.NZBResult{
		Title: "Example",
		Link:  "magnet:?xt=urn:btih:abcdef1234567890",
		Attributes: map[string]string{
			"infoHash": "abcdef1234567890",
		},
	}, true)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if health.Status != "cached" || !health.Cached || !health.Healthy {
		t.Fatalf("expected authoritative cached result, got %#v", health)
	}
	if health.TrackProbeError == "" {
		t.Fatal("expected track preparation failure to be reported separately")
	}
	if got := atomic.LoadInt64(&provider.getInfoCalls); got != 1 {
		t.Fatalf("GetTorrentInfo calls = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&provider.deleteCalls); got != 1 {
		t.Fatalf("DeleteTorrent calls = %d, want 1", got)
	}
}

func TestFullHealthNeverDeletesTorrentUsedByActivePlayback(t *testing.T) {
	provider := &mockProvider{
		name:             "health_active_playback_cleanup",
		cacheStatusKnown: true,
		cachedOnAdd:      false,
	}
	RegisterProvider(provider.name, func(string) Provider { return provider })

	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Name: "Active playback", Provider: provider.name, APIKey: "test-key", Enabled: true},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	hs := NewHealthService(cfg)
	hs.MarkTorrentActive(provider.name, "test-torrent-id")
	health, err := hs.CheckHealth(context.Background(), models.NZBResult{
		Title: "Example",
		Link:  "magnet:?xt=urn:btih:abcdef1234567890",
		Attributes: map[string]string{
			"infoHash": "abcdef1234567890",
		},
	}, true)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if health.Status != "not_cached" {
		t.Fatalf("health status = %q, want not_cached", health.Status)
	}
	if got := atomic.LoadInt64(&provider.deleteCalls); got != 0 {
		t.Fatalf("DeleteTorrent calls = %d, want 0 for active playback", got)
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

func TestPreResolvedHead403FallsBackToRangeGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			http.Error(w, "HEAD forbidden", http.StatusForbidden)
		case http.MethodGet:
			if got := r.Header.Get("Range"); got != "bytes=0-4095" {
				t.Fatalf("Range header = %q, want bytes=0-4095", got)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Range", "bytes 0-4095/4585111806")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(make([]byte, 4096))
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer server.Close()

	hs := NewHealthService(config.NewManager(t.TempDir() + "/settings.json"))
	health, err := hs.CheckHealth(context.Background(), models.NZBResult{
		Title:       "HDHub FSLv2 stream",
		Link:        server.URL + "/video.mkv",
		ServiceType: models.ServiceTypeDebrid,
		Attributes: map[string]string{
			"preresolved": "true",
			"stream_url":  server.URL + "/video.mkv",
			"scraper":     directStremioType,
			"tracker":     "FSLv2",
		},
	}, false)
	if err != nil {
		t.Fatalf("CheckHealth returned error: %v", err)
	}
	if !health.Healthy || !health.Cached {
		t.Fatalf("expected ranged GET to verify stream, got %#v", health)
	}
}

type bulkCacheProvider struct {
	*mockProvider
	cached map[string]bool
}

func (p *bulkCacheProvider) CheckInstantAvailabilityBulk(_ context.Context, hashes []string) (map[string]bool, error) {
	out := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		out[hash] = p.cached[hash]
	}
	return out, nil
}

type unsupportedInstantProvider struct {
	*mockProvider
}

func (p *unsupportedInstantProvider) CheckInstantAvailability(context.Context, string) (bool, error) {
	atomic.AddInt64(&p.instantCalls, 1)
	return false, ErrInstantAvailabilityUnsupported
}

func TestCheckQuickCacheOnlyBulkStopsAtUnsupportedProvider(t *testing.T) {
	bulk := &bulkCacheProvider{
		mockProvider: &mockProvider{name: "health_bulk_supported"},
		cached:       map[string]bool{"aaaa1111": true, "bbbb2222": true},
	}
	unsupported := &unsupportedInstantProvider{mockProvider: &mockProvider{name: "health_bulk_unsupported"}}
	RegisterProvider(bulk.name, func(string) Provider { return bulk })
	RegisterProvider(unsupported.name, func(string) Provider { return unsupported })

	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Streaming.DebridProviders = []config.DebridProviderSettings{
		{Name: "Bulk", Provider: bulk.name, APIKey: "one", Enabled: true},
		{Name: "Unsupported", Provider: unsupported.name, APIKey: "two", Enabled: true},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	hashes := []string{"aaaa1111", "bbbb2222", "cccc3333"}
	results := make([]models.NZBResult, len(hashes))
	for i, hash := range hashes {
		results[i] = models.NZBResult{
			Title:       "Example " + hash,
			ServiceType: models.ServiceTypeDebrid,
			Attributes:  map[string]string{"infoHash": hash},
		}
	}

	out, err := NewHealthService(cfg).CheckQuickCacheOnlyBulk(context.Background(), results)
	if err != nil {
		t.Fatalf("CheckQuickCacheOnlyBulk returned error: %v", err)
	}
	for i := 0; i < 2; i++ {
		if !out[i].Cached || out[i].Status != "cached" || out[i].Provider != bulk.name {
			t.Fatalf("result %d: expected cached on %s, got %#v", i, bulk.name, out[i])
		}
	}
	// Not cached on the provider that answered, unknown on the other: not a definitive miss.
	if out[2].Cached || out[2].Status != "skipped" {
		t.Fatalf("result 2: expected skipped (unknown), got %#v", out[2])
	}
	if got := atomic.LoadInt64(&unsupported.instantCalls); got != 1 {
		t.Fatalf("unsupported provider calls = %d, want 1", got)
	}
}

func TestRealDebridInstantAvailabilityIsUnsupported(t *testing.T) {
	_, err := NewRealDebridClient("test-key").CheckInstantAvailability(context.Background(), "abcdef1234567890")
	if !errors.Is(err, ErrInstantAvailabilityUnsupported) {
		t.Fatalf("expected ErrInstantAvailabilityUnsupported, got %v", err)
	}
}
