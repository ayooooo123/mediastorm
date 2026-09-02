package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestStalkerPortalCatalogAndTuneTimeResolution(t *testing.T) {
	var mu sync.Mutex
	var createLinkCommand string
	var handshakeCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stalker_portal/server/load.php" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(r.Header.Get("Cookie"), "mac=00%3A1A%3A79%3A00%3A00%3A01") {
			t.Errorf("missing encoded MAC cookie: %q", r.Header.Get("Cookie"))
		}
		action := r.URL.Query().Get("action")
		if action != "handshake" && r.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "handshake":
			mu.Lock()
			handshakeCount++
			mu.Unlock()
			fmt.Fprint(w, `{"js":{"token":"token-1"}}`)
		case "get_profile":
			fmt.Fprint(w, `{"js":{"id":"1"}}`)
		case "get_genres":
			fmt.Fprint(w, `{"js":[{"id":"7","title":"News"}]}`)
		case "get_all_channels":
			fmt.Fprint(w, `{"js":{"data":[{"id":"12","name":"World News","number":"1","tv_genre_id":"7","logo":"/logos/news.png","cmd":"ffmpeg http://localhost/ch/12","xmltv_id":"news.world"}]}}`)
		case "create_link":
			mu.Lock()
			createLinkCommand = r.URL.Query().Get("cmd")
			mu.Unlock()
			fmt.Fprint(w, `{"js":{"cmd":"ffmpeg https://cdn.example/live/12.ts?token=signed"}}`)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	config := stalkerSourceConfig{PortalURL: server.URL + "/stalker_portal/c/", MAC: "00:1a:79:00:00:01"}
	channels, err := fetchStalkerChannels(context.Background(), config)
	if err != nil {
		t.Fatalf("fetchStalkerChannels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(channels))
	}
	channel := channels[0]
	if channel.ID != "12" || channel.PlaybackID != "12" || channel.Group != "News" || channel.TvgID != "news.world" {
		t.Fatalf("unexpected channel: %+v", channel)
	}
	if channel.URL != config.PortalURL {
		t.Errorf("channel URL = %q, want logical portal URL %q", channel.URL, config.PortalURL)
	}
	if channel.Logo != server.URL+"/logos/news.png" {
		t.Errorf("logo = %q", channel.Logo)
	}

	streamURL, headers, err := resolveStalkerChannel(context.Background(), config, channel.PlaybackID)
	if err != nil {
		t.Fatalf("resolveStalkerChannel: %v", err)
	}
	if streamURL != "https://cdn.example/live/12.ts?token=signed" {
		t.Errorf("stream URL = %q", streamURL)
	}
	if headers["X-User-Agent"] != "Model: MAG254; Link: Ethernet" {
		t.Errorf("X-User-Agent = %q", headers["X-User-Agent"])
	}
	mu.Lock()
	defer mu.Unlock()
	if createLinkCommand != "ffmpeg http://localhost/ch/12" {
		t.Errorf("create_link cmd = %q", createLinkCommand)
	}
	if handshakeCount != 1 {
		t.Errorf("handshakes = %d, want 1", handshakeCount)
	}
}

func TestProbeStalkerPortalDoesNotFetchCatalog(t *testing.T) {
	var catalogRequested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "handshake":
			fmt.Fprint(w, `{"js":{"token":"probe-token"}}`)
		case "get_profile":
			fmt.Fprint(w, `{"js":{"id":"1"}}`)
		case "get_genres":
			fmt.Fprint(w, `{"js":[{"id":"1","title":"General"},{"id":"2","title":"Sports"}]}`)
		case "get_all_channels", "get_ordered_list":
			catalogRequested = true
			fmt.Fprint(w, `{"js":[]}`)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	genreCount, err := probeStalkerPortal(context.Background(), stalkerSourceConfig{
		PortalURL: server.URL + "/stalker_portal/c/",
		MAC:       "00:1A:79:00:00:02",
	})
	if err != nil {
		t.Fatalf("probeStalkerPortal: %v", err)
	}
	if genreCount != 2 {
		t.Fatalf("genre count = %d, want 2", genreCount)
	}
	if catalogRequested {
		t.Fatal("connection probe unexpectedly fetched the channel catalogue")
	}
}

func TestResolvedLiveSourcesStalker(t *testing.T) {
	sources := resolvedLiveSources(models.ResolvedLiveSource{Sources: []models.LivePlaylistSource{{
		ID: "portal", Name: "Portal", Mode: "stalker", StalkerPortalURL: "https://portal.example/c/", StalkerMAC: "00:1A:79:00:00:01",
	}}})
	if len(sources) != 1 || sources[0].Mode != "stalker" || sources[0].StalkerMAC == "" {
		t.Fatalf("unexpected sources: %+v", sources)
	}
	if got := normalizeLiveProvider("stalker"); got != "stalker" {
		t.Errorf("normalizeLiveProvider(stalker) = %q", got)
	}
}

func TestCleanStalkerStreamCommand(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"ffmpeg http://stream.example/live.ts", "http://stream.example/live.ts"},
		{"ffrt https://stream.example/live.m3u8?x=1", "https://stream.example/live.m3u8?x=1"},
		{"auto http://stream.example/live.ts", "http://stream.example/live.ts"},
	} {
		got, err := cleanStalkerStreamCommand(test.input)
		if err != nil || got != test.want {
			t.Errorf("cleanStalkerStreamCommand(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	if _, err := cleanStalkerStreamCommand("http://stream.example/play/live.php?stream=&token=x"); err == nil {
		t.Fatal("empty stream ID was accepted")
	}
}

func TestStalkerDirectCatalogLinkSkipsCreateLink(t *testing.T) {
	createLinkCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "handshake":
			fmt.Fprint(w, `{"js":{"token":"token-direct"}}`)
		case "get_profile":
			fmt.Fprint(w, `{"js":{"id":"1"}}`)
		case "get_genres":
			fmt.Fprint(w, `{"js":[]}`)
		case "get_all_channels":
			fmt.Fprintf(w, `{"js":{"data":[{"id":"88","name":"Direct","cmd":"ffmpeg %s/play/live.php?stream=88&token=signed"}]}}`, serverURLForTest(r))
		case "create_link":
			createLinkCalled = true
			fmt.Fprint(w, `{"js":{"cmd":""}}`)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	config := stalkerSourceConfig{PortalURL: server.URL + "/stalker_portal/c/", MAC: "00:1A:79:00:00:03"}
	channels, err := fetchStalkerChannels(context.Background(), config)
	if err != nil || len(channels) != 1 {
		t.Fatalf("fetchStalkerChannels = %d, %v", len(channels), err)
	}
	streamURL, _, err := resolveStalkerChannel(context.Background(), config, "88")
	if err != nil {
		t.Fatalf("resolveStalkerChannel: %v", err)
	}
	if !strings.Contains(streamURL, "stream=88") {
		t.Fatalf("stream URL lost channel ID: %q", streamURL)
	}
	if createLinkCalled {
		t.Fatal("direct catalogue link was unnecessarily sent through create_link")
	}
}

func serverURLForTest(r *http.Request) string {
	return "http://" + r.Host
}

func TestStalkerSettingsAreRedactedAndRestored(t *testing.T) {
	existing := config.DefaultSettings()
	existing.Live.StalkerPortalURL = "https://portal.example/c/"
	existing.Live.StalkerMAC = "00:1A:79:12:34:56"
	existing.Live.StalkerSerialNumber = "serial-secret"
	existing.Live.StalkerDeviceID = "device-secret"
	existing.Live.StalkerDeviceID2 = "device2-secret"
	existing.Live.StalkerSignature = "signature-secret"
	existing.Live.Sources = []config.LivePlaylistSource{{
		Mode: "stalker", StalkerPortalURL: existing.Live.StalkerPortalURL, StalkerMAC: existing.Live.StalkerMAC,
		StalkerSerialNumber: existing.Live.StalkerSerialNumber, StalkerDeviceID: existing.Live.StalkerDeviceID,
		StalkerDeviceID2: existing.Live.StalkerDeviceID2, StalkerSignature: existing.Live.StalkerSignature,
	}}
	redacted := existing
	redactSettings(&redacted)
	values := []string{
		redacted.Live.StalkerPortalURL, redacted.Live.StalkerMAC, redacted.Live.StalkerSerialNumber,
		redacted.Live.StalkerDeviceID, redacted.Live.StalkerDeviceID2, redacted.Live.StalkerSignature,
		redacted.Live.Sources[0].StalkerPortalURL, redacted.Live.Sources[0].StalkerMAC,
	}
	for _, value := range values {
		if value != redactedPlaceholder {
			t.Fatalf("Stalker setting was not redacted: %q", value)
		}
	}
	preserveRedactedFields(&redacted, &existing)
	if redacted.Live.StalkerMAC != existing.Live.StalkerMAC || redacted.Live.Sources[0].StalkerSignature != existing.Live.Sources[0].StalkerSignature {
		t.Fatal("Stalker settings were not restored after redacted save-back")
	}
}
