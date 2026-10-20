package jellyfinbridge

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// PlaybackInfo answers after a bounded wait; a stream that takes longer to
// resolve must not answer direct players (mpv requests the file right away
// and retries only briefly) with 503, but wait like the HLS playlist does.
func TestDirectStreamWaitsForSlowResolution(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{PlaybackInfoWaitSeconds: 1})
	installFakePlayback(t, h)
	var readyMu sync.Mutex
	readyAt := time.Now().Add(time.Hour)
	ready := func() time.Time {
		readyMu.Lock()
		defer readyMu.Unlock()
		return readyAt
	}
	h.srv.deps.Handlers.PrequeueStatus = func(w http.ResponseWriter, r *http.Request) {
		if time.Now().Before(ready()) {
			writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{PrequeueID: "pq1", Status: playback.PrequeueStatusResolving})
			return
		}
		writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{PrequeueID: "pq1", Status: playback.PrequeueStatusReady, StreamPath: "/webdav/movie.mkv", Duration: 120})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	// PlaybackInfo gives up waiting after a second; the stream is not ready.
	readyMu.Lock()
	readyAt = time.Now().Add(2500 * time.Millisecond)
	readyMu.Unlock()
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{}))
	if len(info.MediaSources) != 1 || time.Now().After(ready()) {
		t.Fatalf("playback info = %+v (test timing off)", info)
	}
	q := url.Values{"Static": {"true"}, "api_key": {tok}, "MediaSourceId": {info.MediaSources[0].ID}}
	start := time.Now()
	rec := h.do(http.MethodGet, "/Videos/"+item+"/stream?"+q.Encode(), "", nil)
	if rec.Code >= 300 {
		t.Fatalf("stream = %d %s, want it to wait for resolution", rec.Code, rec.Body.String())
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatal("the stream request should have waited for resolution")
	}
}
