package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tsPacket(counter byte) []byte {
	packet := make([]byte, mpegTSPacketSize)
	packet[0] = mpegTSSyncByte
	for i := 1; i < len(packet); i++ {
		packet[i] = counter
	}
	return packet
}

// liveTSServer streams TS packets until the client goes away. dropAfter > 0 ends the first
// response after that many packets, imitating a provider closing the connection.
type liveTSServer struct {
	requests  atomic.Int32
	open      atomic.Int32
	dropAfter int
	closed    chan struct{}
	closeOnce sync.Once
}

func newLiveTSServer(t *testing.T, dropAfter int) (*liveTSServer, *httptest.Server) {
	t.Helper()
	state := &liveTSServer{dropAfter: dropAfter, closed: make(chan struct{}, 16)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := state.requests.Add(1)
		state.open.Add(1)
		defer func() {
			state.open.Add(-1)
			state.closed <- struct{}{}
		}()
		w.Header().Set("Content-Type", "video/mp2t")
		flusher := w.(http.Flusher)
		for i := 0; ; i++ {
			if request == 1 && state.dropAfter > 0 && i >= state.dropAfter {
				return
			}
			if _, err := w.Write(tsPacket(byte(i))); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(server.Close)
	return state, server
}

func newTestLiveUpstreamHub() *liveUpstreamHub {
	return &liveUpstreamHub{streams: make(map[string]*liveUpstream)}
}

func readAtLeast(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	done := make(chan []byte, 1)
	errs := make(chan error, 1)
	go func() {
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			errs <- err
			return
		}
		done <- buf
	}()
	select {
	case buf := <-done:
		return buf
	case err := <-errs:
		t.Fatalf("read failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading from shared upstream")
	}
	return nil
}

func liveTestSession(url string) *HLSSession {
	return &HLSSession{ID: "test", Path: url, IsLive: true}
}

func TestLiveUpstreamSessionsOnOneChannelShareOneConnection(t *testing.T) {
	state, server := newLiveTSServer(t, 0)
	hub := newTestLiveUpstreamHub()
	session := liveTestSession(server.URL + "/live/user/pass/1.ts")
	key := liveUpstreamShareKey(session.Path, session.LiveTuning)

	first, shared, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if err != nil || shared {
		t.Fatalf("first subscribe: shared=%v err=%v", shared, err)
	}
	defer first.Close()
	readAtLeast(t, first, 10*mpegTSPacketSize)

	second, shared, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if err != nil || !shared {
		t.Fatalf("second subscribe: shared=%v err=%v", shared, err)
	}
	defer second.Close()
	joined := readAtLeast(t, second, 10*mpegTSPacketSize)
	if joined[0] != mpegTSSyncByte || joined[mpegTSPacketSize] != mpegTSSyncByte {
		t.Fatalf("late joiner did not start on a TS packet boundary: % x", joined[:4])
	}
	readAtLeast(t, first, 10*mpegTSPacketSize)

	if got := state.requests.Load(); got != 1 {
		t.Fatalf("provider saw %d connections, want 1", got)
	}
	if got := hub.Subscribers(key); got != 2 {
		t.Fatalf("subscribers = %d, want 2", got)
	}
}

func TestLiveUpstreamClosesWhenLastViewerLeaves(t *testing.T) {
	state, server := newLiveTSServer(t, 0)
	hub := newTestLiveUpstreamHub()
	session := liveTestSession(server.URL + "/live/1.ts")
	key := liveUpstreamShareKey(session.Path, session.LiveTuning)

	first, _, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if err != nil {
		t.Fatal(err)
	}
	readAtLeast(t, second, mpegTSPacketSize)

	first.Close()
	readAtLeast(t, second, 10*mpegTSPacketSize) // the remaining viewer keeps playing
	if got := state.open.Load(); got != 1 {
		t.Fatalf("open provider connections = %d after one viewer left, want 1", got)
	}

	second.Close()
	select {
	case <-state.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("provider connection stayed open after the last viewer left")
	}
	if got := hub.Subscribers(key); got != 0 {
		t.Fatalf("subscribers = %d after close, want 0", got)
	}
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("closed subscriber still readable")
	}

	// The next viewer opens a fresh connection.
	third, shared, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if err != nil || shared {
		t.Fatalf("resubscribe: shared=%v err=%v", shared, err)
	}
	defer third.Close()
	readAtLeast(t, third, mpegTSPacketSize)
	if got := state.requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
}

func TestLiveUpstreamReconnectsWithoutEndingViewers(t *testing.T) {
	state, server := newLiveTSServer(t, 20)
	hub := newTestLiveUpstreamHub()
	session := liveTestSession(server.URL + "/live/1.ts")
	key := liveUpstreamShareKey(session.Path, session.LiveTuning)

	sub, _, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	readAtLeast(t, sub, 60*mpegTSPacketSize)
	if got := state.requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (one reconnect)", got)
	}
}

func TestLiveUpstreamGivesUpAfterRepeatedFailures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Write(tsPacket(1))
			return
		}
		http.Error(w, "max connections", http.StatusForbidden)
	}))
	defer server.Close()
	hub := newTestLiveUpstreamHub()
	session := liveTestSession(server.URL + "/live/1.ts")
	key := liveUpstreamShareKey(session.Path, session.LiveTuning)

	sub, _, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, sub)
		if err == nil {
			err = io.EOF
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subscriber never ended after the provider kept refusing")
	}
	if got := hub.Subscribers(key); got != 0 {
		t.Fatalf("failed upstream still registered with %d subscribers", got)
	}
	if got := requests.Load(); got != 1+liveUpstreamMaxReconnects {
		t.Fatalf("provider requests = %d, want %d", got, 1+liveUpstreamMaxReconnects)
	}
}

func TestLiveUpstreamRejectsHLSPlaylistSoFFmpegReadsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n")
	}))
	defer server.Close()
	hub := newTestLiveUpstreamHub()
	session := liveTestSession(server.URL + "/live/1")
	key := liveUpstreamShareKey(session.Path, session.LiveTuning)

	_, _, err := hub.Subscribe(key, "test", openLiveUpstream(session))
	if !errors.Is(err, errLiveUpstreamNotTS) {
		t.Fatalf("err = %v, want errLiveUpstreamNotTS", err)
	}
	if got := hub.Subscribers(key); got != 0 {
		t.Fatalf("failed open left %d subscribers", got)
	}
}

func TestLiveUpstreamShareKey(t *testing.T) {
	base := "http://provider.example/live/u/p/1.ts"
	if liveUpstreamShareKey(base, LiveTuningSettings{}) == "" {
		t.Fatal("plain TS channel should be shareable")
	}
	for _, path := range []string{
		"http://provider.example/live/u/p/1.m3u8",
		"https://addon.example/playlist/abc",
		"rtmp://provider.example/live/1",
		"udp://239.0.0.1:1234",
	} {
		if key := liveUpstreamShareKey(path, LiveTuningSettings{}); key != "" {
			t.Fatalf("%s should not be shared, got key %q", path, key)
		}
	}
	if liveUpstreamShareKey(base, LiveTuningSettings{ForceHLSInput: true}) != "" {
		t.Fatal("forced HLS input should not be shared")
	}
	plain := liveUpstreamShareKey(base, LiveTuningSettings{})
	withHeader := liveUpstreamShareKey(base, LiveTuningSettings{RequestHeaders: map[string]string{"Referer": "x"}})
	withProxy := liveUpstreamShareKey(base, LiveTuningSettings{ProxyURL: "socks5://proxy:1080"})
	if plain == withHeader || plain == withProxy {
		t.Fatal("different request headers or proxies must not share a connection")
	}
}

func TestAlignToTransportStream(t *testing.T) {
	stream := append(append([]byte{0x11, 0x47, 0x22}, tsPacket(1)...), tsPacket(2)...)
	aligned := alignToTransportStream(stream)
	if !bytes.Equal(aligned[:mpegTSPacketSize], tsPacket(1)) {
		t.Fatalf("aligned stream starts with % x", aligned[:4])
	}
}

func TestGetLiveUsageCountsSharedUpstreamOnce(t *testing.T) {
	shared := liveUpstreamShareKey("http://provider.example/live/1.ts", LiveTuningSettings{})
	other := liveUpstreamShareKey("http://provider.example/live/2.ts", LiveTuningSettings{})
	m := &HLSManager{sessions: map[string]*HLSSession{
		"tv":    {IsLive: true, LiveProvider: "xtream", LiveBucket: "xtream:a", LiveUpstreamKey: shared},
		"phone": {IsLive: true, LiveProvider: "xtream", LiveBucket: "xtream:a", LiveUpstreamKey: shared},
		"other": {IsLive: true, LiveProvider: "xtream", LiveBucket: "xtream:a", LiveUpstreamKey: other},
		"hls":   {IsLive: true, LiveProvider: "xtream", LiveBucket: "xtream:a"},
	}}
	usage := m.GetLiveUsage("xtream", "xtream:a", 3)
	if usage.CurrentStreams != 3 {
		t.Fatalf("current streams = %d, want 3 (two viewers on one channel use one slot)", usage.CurrentStreams)
	}
	if !usage.AtLimit {
		t.Fatal("expected the provider to be at its 3-stream limit")
	}
}

func TestLiveSessionsOnOneChannelTranscodeFromOneProviderConnection(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "channel.ts")
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "4", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-f", "mpegts", fixture)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("generate fixture: %v %s", err, output)
	}
	ts, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		flusher := w.(http.Flusher)
		// Loop the clip at roughly realtime, like a channel that never ends.
		const step = 188 * 40
		for {
			for offset := 0; offset < len(ts); offset += step {
				end := offset + step
				if end > len(ts) {
					end = len(ts)
				}
				if _, err := w.Write(ts[offset:end]); err != nil {
					return
				}
				flusher.Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		}
	}))
	defer server.Close()

	m := &HLSManager{ffmpegPath: ffmpeg}
	channel := server.URL + "/live/user/pass/42.ts"
	start := func(id string) (*HLSSession, chan error) {
		session := &HLSSession{
			ID: id, Path: channel, LiveProvider: "xtream", IsLive: true,
			OutputDir: filepath.Join(dir, id), PlaybackTarget: "native", LastSegmentRequest: time.Now(),
		}
		done := make(chan error, 1)
		go func() { done <- m.startLiveTranscoding(ctx, session, 0) }()
		return session, done
	}
	waitForSegments := func(session *HLSSession, n int) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if nextLiveSegmentNumber(session.OutputDir) >= n {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("session %s produced fewer than %d segments", session.ID, n)
	}

	tv, tvDone := start("tv")
	waitForSegments(tv, 1)
	phone, phoneDone := start("phone")
	waitForSegments(phone, 2)
	waitForSegments(tv, 3)

	if got := requests.Load(); got != 1 {
		t.Fatalf("provider saw %d connections for two viewers of one channel, want 1", got)
	}
	cancel()
	<-tvDone
	<-phoneDone
}
