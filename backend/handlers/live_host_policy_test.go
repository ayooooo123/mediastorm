package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/requestsecurity"
)

func TestLivePlaylistPrivateSourceChangesWithoutRestart(t *testing.T) {
	newPlaylistServer := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "audio/x-mpegurl")
			w.Header().Set("Content-Disposition", "attachment; filename=lineup.m3u")
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1," + name + "\nhttp://192.168.1.100:5004/auto/v5.1\n"))
		}))
	}
	first := newPlaylistServer("First channel")
	defer first.Close()
	second := newPlaylistServer("Second channel")
	defer second.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.Settings{}
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	h := NewLiveHandler(nil, false, "", 24, 0, 0, false, mgr, nil)
	defer h.client.CloseIdleConnections()
	h.cacheTTL = time.Nanosecond
	for _, source := range []struct {
		server *httptest.Server
		name   string
	}{{first, "First channel"}, {second, "Second channel"}} {
		settings.Live.Sources = []config.LivePlaylistSource{{ID: "tuner", Mode: "m3u", PlaylistURL: source.server.URL + "/lineup.m3u"}}
		if err := mgr.Save(settings); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		h.GetChannels(response, httptest.NewRequest(http.MethodGet, "/live/channels?sourceId=tuner", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("new private source: status=%d body=%s", response.Code, response.Body.String())
		}
		var result LiveChannelsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Channels) != 1 || result.Channels[0].Name != source.name {
			t.Fatalf("channels = %+v, want %q", result.Channels, source.name)
		}
	}

	// New connections must stop trusting an origin removed from configuration.
	h.client.CloseIdleConnections()
	if resp, err := h.client.Get(first.URL + "/lineup.m3u"); err == nil {
		resp.Body.Close()
		t.Fatal("removed private source is still allowed by the transport")
	}
}

func TestLivePlaylistRedirectUsesUpdatedPrivateSources(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1,Redirected channel\nhttp://192.168.1.100:5004/auto/v5.1\n"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/lineup.m3u", http.StatusFound)
	}))
	defer origin.Close()

	for _, suppliedClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "default client", true: "supplied client"}[suppliedClient], func(t *testing.T) {
			mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			settings := config.Settings{Live: config.LiveSettings{PlaylistURL: origin.URL + "/lineup.m3u"}}
			if err := mgr.Save(settings); err != nil {
				t.Fatal(err)
			}
			var client *http.Client
			if suppliedClient {
				client = origin.Client()
			}
			h := NewLiveHandler(client, false, "", 24, 0, 0, false, mgr, nil)
			defer h.client.CloseIdleConnections()
			settings.Live.Sources = []config.LivePlaylistSource{{PlaylistURL: target.URL + "/lineup.m3u"}}
			if err := mgr.Save(settings); err != nil {
				t.Fatal(err)
			}
			resp, err := h.client.Get(origin.URL + "/lineup.m3u")
			if err != nil {
				t.Fatalf("redirect to newly configured private source: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
		})
	}
}

func TestHDHomeRunDoesNotGrantGenericStreamOrigin(t *testing.T) {
	disabled := false
	for _, tc := range []struct {
		name string
		live config.LiveSettings
		want bool
	}{
		{"global playlist", config.LiveSettings{PlaylistURL: "http://192.168.1.100/lineup.m3u"}, false},
		{"explicit HTTP port", config.LiveSettings{PlaylistURL: "http://192.168.1.100:80/lineup.m3u?favorites"}, false},
		{"source playlist", config.LiveSettings{Sources: []config.LivePlaylistSource{{PlaylistURL: "http://192.168.1.100/lineup.m3u"}}}, false},
		{"legacy source playlist", config.LiveSettings{PlaylistSources: []config.LivePlaylistSource{{PlaylistURL: "http://192.168.1.100/lineup.m3u"}}}, false},
		{"disabled source", config.LiveSettings{Sources: []config.LivePlaylistSource{{PlaylistURL: "http://192.168.1.100/lineup.m3u", Enabled: &disabled}}}, false},
		{"ordinary playlist", config.LiveSettings{PlaylistURL: "http://192.168.1.100/channels.m3u"}, false},
		{"nested playlist", config.LiveSettings{PlaylistURL: "http://192.168.1.100/provider/lineup.m3u"}, false},
		{"invalid scheme", config.LiveSettings{PlaylistURL: "file://192.168.1.100/lineup.m3u"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := configuredProviderHostPolicy(staticSecurityConfigProvider{settings: config.Settings{Live: tc.live}})
			err := requestsecurity.ValidateOutboundURL(t.Context(), "http://192.168.1.100:5004/auto/v5.1", policy)
			if (err == nil) != tc.want {
				t.Fatalf("stream URL allowed=%v, want %v; error=%v", err == nil, tc.want, err)
			}
			for _, blocked := range []string{
				"http://192.168.1.101:5004/auto/v5.1",
				"http://192.168.1.100:7777/api/settings",
			} {
				if err := requestsecurity.ValidateOutboundURL(t.Context(), blocked, policy); err == nil {
					t.Fatalf("unconfigured origin allowed: %s", blocked)
				}
			}
		})
	}
}

func TestStartLiveHDHomeRunSessionAllowsTunerStreamPort(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("transport-stream-fixture"))
	}))
	defer proxy.Close()
	for _, tc := range []struct {
		format, target string
		wantHLS        bool
	}{
		{"direct", "native", false},
		{"direct", "web", true},
		{"direct", "cast", true},
		{"hls", "native", true},
		{"hls", "web", true},
		{"hls", "cast", true},
	} {
		t.Run(tc.format+"/"+tc.target, func(t *testing.T) {
			// Stub FFmpeg: this checks session authorization, not tuner playback.
			h := NewVideoHandlerWithProvider(true, "/usr/bin/true", "/usr/bin/true", t.TempDir(), nil)
			t.Cleanup(h.hlsManager.Shutdown)
			h.SetConfigManager(fakeLiveUsageConfigProvider{settings: config.Settings{
				Live: config.LiveSettings{Mode: "m3u", PlaylistURL: "http://192.168.1.100/lineup.m3u", StreamFormat: tc.format, ProxyURL: proxy.URL},
			}})
			streamURL := "http://192.168.1.100:5004/auto/v5.1"
			h.SetLiveChannelProvider(staticLiveChannelProvider{channels: []LiveChannel{{ID: "channel-5", SourceID: "default", URL: streamURL}}})
			request := httptest.NewRequest(http.MethodGet, "/live/hls/start?target="+tc.target+"&url="+url.QueryEscape(streamURL), nil)
			response := httptest.NewRecorder()
			h.StartLiveHLSSession(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var result struct {
				SessionID string `json:"sessionId"`
				StreamURL string `json:"streamUrl"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if tc.wantHLS {
				session, ok := h.hlsManager.GetSession(result.SessionID)
				if !ok || session.Path != streamURL || !session.LiveTuning.HDHomeRunInput || session.PlaybackTarget != tc.target {
					t.Fatal("HLS session did not retain the tuner stream URL")
				}
			} else if !strings.HasPrefix(result.StreamURL, "/live/stream?") {
				t.Fatalf("direct stream URL=%q", result.StreamURL)
			}
		})
	}
}

// Cast receivers get the managed live HLS session even when the source is
// configured direct; upstream forces HLS for Cast only on HDHomeRun input.
func TestStartLiveOrdinaryDirectCastForcesHLS(t *testing.T) {
	h := NewVideoHandlerWithProvider(true, "/usr/bin/true", "/usr/bin/true", t.TempDir(), nil)
	t.Cleanup(h.hlsManager.Shutdown)
	h.SetConfigManager(fakeLiveUsageConfigProvider{settings: config.Settings{
		Live: config.LiveSettings{Mode: "m3u", PlaylistURL: "https://93.184.216.34/channels.m3u", StreamFormat: "direct"},
	}})
	response := httptest.NewRecorder()
	h.StartLiveHLSSession(response, httptest.NewRequest(http.MethodGet,
		"/live/hls/start?target=cast&url="+url.QueryEscape("https://93.184.216.34/channel.ts"), nil))
	var result struct {
		IsDirect    bool   `json:"isDirect"`
		PlaylistURL string `json:"playlistUrl"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.IsDirect || !strings.HasPrefix(result.PlaylistURL, "/video/hls/") {
		t.Fatalf("ordinary direct Cast response: status=%d body=%s", response.Code, response.Body.String())
	}
}
