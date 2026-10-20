package jellyfinbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// fakePlayback wires fake mediastorm handlers into a harness and records
// what the bridge asked them to do.
type fakePlayback struct {
	mu          sync.Mutex
	hlsStarts   int32
	hlsStops    []string
	streamQuery url.Values
	streamRange string
	startStatus int // non-zero: HLS start fails with this status
}

func installFakePlayback(t *testing.T, h *harness) *fakePlayback {
	t.Helper()
	fp := &fakePlayback{}
	hd := &h.srv.deps.Handlers
	hd.Prequeue = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.PrequeueResponse{PrequeueID: "pq1", Status: playback.PrequeueStatusQueued})
	}
	hd.PrequeueStatus = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{
			PrequeueID: "pq1", Status: playback.PrequeueStatusReady, StreamPath: "/webdav/movie.mkv",
			Duration: 120, SelectedAudioTrack: 1, SelectedSubtitleTrack: -1,
			AudioTracks: []playback.AudioTrackInfo{{Index: 1, Codec: "aac", Language: "eng"}},
		})
	}
	hd.HLSStart = func(w http.ResponseWriter, r *http.Request) {
		if fp.startStatus != 0 {
			http.Error(w, "nope", fp.startStatus)
			return
		}
		if got := r.URL.Query().Get("target"); got != "web-stable" {
			t.Errorf("HLS start target = %q, want web-stable", got)
		}
		if r.URL.Query().Get("startOffset") != "" {
			t.Error("HLS session must start at 0; clients seek inside the playlist")
		}
		n := atomic.AddInt32(&fp.hlsStarts, 1)
		time.Sleep(20 * time.Millisecond) // widen the window for concurrent requests
		writeJSON(w, http.StatusOK, map[string]any{"sessionId": "abc" + string(rune('0'+n)), "playlistUrl": "/video/hls/x/stream.m3u8", "duration": 120.0})
	}
	hd.HLSKeepAlive = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	hd.HLSStop = func(w http.ResponseWriter, r *http.Request) {
		fp.mu.Lock()
		fp.hlsStops = append(fp.hlsStops, r.URL.Path)
		fp.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
	hd.HLSPlaylist = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:4.0,\nsegment0.ts?token="+r.URL.Query().Get("token")+"\n#EXT-X-ENDLIST\n")
	}
	hd.HLSSegment = func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "segment-bytes")
	}
	hd.Stream = func(w http.ResponseWriter, r *http.Request) {
		fp.mu.Lock()
		fp.streamQuery = r.URL.Query()
		fp.streamRange = r.Header.Get("Range")
		fp.mu.Unlock()
		w.WriteHeader(http.StatusPartialContent)
	}
	return fp
}

func (h *harness) doWithClient(method, path, token, client string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader io.Reader = strings.NewReader("")
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	auth := `MediaBrowser Client="` + client + `", Device="dev", DeviceId="dev-` + client + `", Version="1"`
	if token != "" {
		auth += `, Token="` + token + `"`
	}
	req.Header.Set("Authorization", auth)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func movieID(t *testing.T, h *harness, tok string) string {
	t.Helper()
	views := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/UserViews", tok, nil))
	for _, v := range views.Items {
		if v.Name == "My Movies" {
			items := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?ParentId="+v.ID, tok, nil))
			return items.Items[0].ID
		}
	}
	t.Fatal("no movie")
	return ""
}

func TestHLSPlaybackFlow(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	fp := installFakePlayback(t, h)
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"StartTimeTicks": 600000000}))
	src := info.MediaSources[0]
	if src.TranscodingSubProtocol != "hls" || src.SupportsDirectPlay || !strings.Contains(src.TranscodingURL, "/master.m3u8?") {
		t.Fatalf("web media source = %+v", src)
	}

	// Duplicate concurrent playlist requests must create one HLS session.
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = h.do(http.MethodGet, src.TranscodingURL, "", nil).Code
		}(i)
	}
	wg.Wait()
	if atomic.LoadInt32(&fp.hlsStarts) != 1 {
		t.Fatalf("HLS sessions started = %d, want 1 (codes %v)", fp.hlsStarts, codes)
	}
	rec := h.do(http.MethodGet, src.TranscodingURL, "", nil)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/hls/abc1/stream.m3u8") {
		t.Fatalf("master redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// The playlist and segments pass through for the owner only.
	location := rec.Header().Get("Location")
	if rec := h.do(http.MethodGet, location, "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "segment0.ts?token="+tok) {
		t.Fatalf("playlist passthrough = %d %q", rec.Code, rec.Body.String())
	}
	segment := strings.Replace(strings.Split(location, "?")[0], "stream.m3u8", "segment0.ts", 1) + "?token=" + url.QueryEscape(tok)
	if rec := h.do(http.MethodGet, segment, "", nil); rec.Code != http.StatusOK || rec.Body.String() != "segment-bytes" {
		t.Fatalf("segment passthrough = %d", rec.Code)
	}
	other := h.login("bob/Kids", "bob-pass")
	otherSegment := strings.Replace(segment, url.QueryEscape(tok), url.QueryEscape(other), 1)
	if rec := h.do(http.MethodGet, otherSegment, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("another profile read the HLS stream: %d", rec.Code)
	}

	// Stopping playback stops the mediastorm HLS session.
	h.do(http.MethodPost, "/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": item, "PlaySessionId": info.PlaySessionID, "PositionTicks": 0})
	if len(fp.hlsStops) != 1 || !strings.Contains(fp.hlsStops[0], "abc1") {
		t.Fatalf("HLS stops = %v", fp.hlsStops)
	}
}

func TestDefinitiveHLSFailureDropsPlaySession(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	fp := installFakePlayback(t, h)
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	fp.startStatus = http.StatusInternalServerError // transient
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{}))
	if rec := h.do(http.MethodGet, info.MediaSources[0].TranscodingURL, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("transient failure = %d", rec.Code)
	}
	if _, ok := h.srv.plays.get(info.PlaySessionID); !ok {
		t.Fatal("a transient failure dropped the play session")
	}
	fp.startStatus = http.StatusGone // expired source: definitive
	h.do(http.MethodGet, info.MediaSources[0].TranscodingURL, "", nil)
	if _, ok := h.srv.plays.get(info.PlaySessionID); ok {
		t.Fatal("a definitive failure kept the dead play session")
	}
}

func TestDirectPlayForNativeClients(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	fp := installFakePlayback(t, h)
	tok := decode[dto.AuthenticationResult](t, h.doWithClient(http.MethodPost, "/Users/AuthenticateByName", "", "Android TV", map[string]string{"Username": "Living Room", "Pw": "alice-pass"})).AccessToken
	item := movieID(t, h, tok)

	info := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{}))
	src := info.MediaSources[0]
	if !src.SupportsDirectPlay {
		t.Fatalf("native client got no direct play: %+v", src)
	}
	// Android TV sends no token on media URLs; the media source ID authorizes.
	req := httptest.NewRequest(http.MethodGet, "/Videos/"+item+"/stream?static=true&mediaSourceId="+src.ID, nil)
	req.Header.Set("Range", "bytes=0-99")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || fp.streamQuery.Get("transmux") != "0" || fp.streamQuery.Get("path") != "/webdav/movie.mkv" || fp.streamRange != "bytes=0-99" {
		t.Fatalf("direct play = %d query=%v range=%q", rec.Code, fp.streamQuery, fp.streamRange)
	}
	// Active Streams shows who plays what, as for HLS sessions.
	if q := fp.streamQuery; q.Get("profileName") != "Living Room" || q.Get("mediaType") != "movie" || q.Get("title") == "" || q.Get("clientId") != "jellyfin-dev-Android TV" {
		t.Fatalf("direct play lacks dashboard details: %v", q)
	}

	// A retry with EnableDirectPlay=false (after a failed attempt) must not loop.
	retry := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{"EnableDirectPlay": false}))
	if retry.MediaSources[0].SupportsDirectPlay {
		t.Fatal("direct play offered again after the client disabled it")
	}
	// A device profile that cannot play MKV gets no direct play.
	profile := map[string]any{"DirectPlayProfiles": []map[string]string{{"Container": "mp4", "Type": "Video"}}}
	mp4Only := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{"DeviceProfile": profile}))
	if mp4Only.MediaSources[0].SupportsDirectPlay {
		t.Fatal("direct play offered for a container outside the device profile")
	}

	// Logging out revokes token-less direct play too.
	h.doWithClient(http.MethodPost, "/Sessions/Logout", tok, "Android TV", nil)
	req = httptest.NewRequest(http.MethodGet, "/Videos/"+item+"/stream?static=true&mediaSourceId="+src.ID, nil)
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusPartialContent {
		t.Fatal("token-less direct play survived logout")
	}
}

func TestIDStoreTrust(t *testing.T) {
	s := newIDStore("")
	defer s.close()
	id := s.put(refMovie, "tmdb:movie:603", refContext{Name: "Fake", ExternalIDs: map[string]string{"imdb": "tt0000001"}})
	s.put(refMovie, "tmdb:movie:603", refContext{Name: "Another fake", ExternalIDs: map[string]string{"imdb": "tt9999999", "tvdb": "1"}})
	ref, _ := s.get(id)
	if ref.Ctx.Name != "Fake" || ref.Ctx.ExternalIDs["imdb"] != "tt0000001" || ref.Ctx.ExternalIDs["tvdb"] != "1" {
		t.Fatalf("untrusted put must only fill gaps: %+v", ref.Ctx)
	}
	s.putTrusted(refMovie, "tmdb:movie:603", refContext{Name: "The Matrix", ExternalIDs: map[string]string{"imdb": "tt0133093"}})
	ref, _ = s.get(id)
	if ref.Ctx.Name != "The Matrix" || ref.Ctx.ExternalIDs["imdb"] != "tt0133093" {
		t.Fatalf("trusted put must replace: %+v", ref.Ctx)
	}
	// Identity derived from the canonical source beats stored context.
	ext := sourceExternalIDs(titleInfo{MediaType: "movie", ID: "tmdb:movie:603", ExternalIDs: map[string]string{"tmdb": "999", "imdb": "tt1"}})
	if ext["tmdb"] != "603" {
		t.Fatalf("source tmdb id overridden: %v", ext)
	}
}

func TestSocketKeepAlive(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /socket?api_key="+url.QueryEscape(tok)+" HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake: %v %+v", err, resp)
	}
	op, payload, err := readFrame(br)
	if err != nil || op != 0x1 || !strings.Contains(string(payload), `"ForceKeepAlive"`) || !strings.Contains(string(payload), `"MessageId"`) {
		t.Fatalf("first frame: op=%d %s err=%v", op, payload, err)
	}
	// Masked client KeepAlive gets a KeepAlive reply.
	msg := []byte(`{"MessageType":"KeepAlive"}`)
	mask := []byte{1, 2, 3, 4}
	frame := []byte{0x81, 0x80 | byte(len(msg))}
	frame = append(frame, mask...)
	for i, b := range msg {
		frame = append(frame, b^mask[i%4])
	}
	_, _ = conn.Write(frame)
	op, payload, err = readFrame(br)
	if err != nil || op != 0x1 || !strings.Contains(string(payload), `"KeepAlive"`) {
		t.Fatalf("keepalive reply: op=%d %s err=%v", op, payload, err)
	}
	// Oversized frames close the connection.
	_, _ = conn.Write([]byte{0x81, 0xFF, 0, 0, 0, 0, 0x7F, 0, 0, 0, 1, 2, 3, 4})
	if _, _, err := readFrame(br); err == nil {
		t.Fatal("oversized frame accepted")
	}
	_ = context.Background
}
