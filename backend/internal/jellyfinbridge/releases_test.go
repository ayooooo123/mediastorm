package jellyfinbridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

func TestReleaseVersionsFollowRankingAndPlay(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 15})
	installFakePlayback(t, h)
	searches := 0
	h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		searches++
		if r.URL.Query().Get("mediaType") != "movie" || r.URL.Query().Get("q") == "" {
			t.Errorf("search query = %v", r.URL.Query())
		}
		results := []map[string]any{}
		for i := 1; i <= 20; i++ {
			res := map[string]any{"title": fmt.Sprintf("Movie.2024.1080p.WEB-DL.H264-GRP%02d", i), "guid": fmt.Sprintf("g%d", i), "sizeBytes": int64(i) << 30, "serviceType": "usenet", "indexer": "nzbgeek"}
			if i == 2 {
				res["filterStatus"] = "filtered"
			}
			results = append(results, res)
		}
		writeJSON(w, http.StatusOK, results)
	}
	var resolved map[string]any
	h.srv.deps.Handlers.PlaybackResolve = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &resolved)
		writeJSON(w, http.StatusOK, map[string]any{"queueId": 7})
	}
	h.srv.deps.Handlers.PlaybackQueue = func(w http.ResponseWriter, r *http.Request) {
		if pathVar(r, "queueID") != "7" {
			t.Errorf("queue id = %q", pathVar(r, "queueID"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"queueId": 7, "webdavPath": "/webdav/release04.mkv", "fileSize": 4 << 30})
	}
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"durationSeconds": 5400,
			"videoStreams":    []map[string]any{{"index": 0, "codecName": "hevc", "colorTransfer": "smpte2084"}},
			"audioStreams":    []map[string]any{{"index": 1, "codecName": "eac3", "language": "ger"}},
			"subtitleStreams": []map[string]any{{"index": 2, "codecName": "subrip", "language": "eng"}},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	// The first details request starts the search; the versions appear once it is done.
	var got dto.BaseItemDto
	for deadline := time.Now().Add(5 * time.Second); ; {
		got = decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
		if len(got.MediaSources) > 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got.MediaSources) != 1+15 {
		t.Fatalf("versions = %d, want auto + 15", len(got.MediaSources))
	}
	if searches != 1 {
		t.Fatalf("searches = %d, want 1 (cached)", searches)
	}
	if !strings.HasPrefix(got.MediaSources[0].Name, "Auto") || got.MediaSources[0].ID != item {
		t.Fatalf("first version = %q %s, want Auto with the item ID", got.MediaSources[0].Name, got.MediaSources[0].ID)
	}
	// jellyfin-web loads the selected version as an item of its own.
	if v := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+got.MediaSources[3].ID, tok, nil)); v.ID != got.MediaSources[3].ID || len(v.MediaSources) != len(got.MediaSources) {
		t.Fatalf("version item = %s with %d versions", v.ID, len(v.MediaSources))
	}
	// Ranking order, filtered results skipped.
	if !strings.HasPrefix(got.MediaSources[1].Name, "#1 1080p H.264 · WEB-DL · GRP01 · 1.0 GB") ||
		!strings.HasPrefix(got.MediaSources[2].Name, "#2 1080p H.264 · WEB-DL · GRP03") ||
		!strings.HasPrefix(got.MediaSources[15].Name, "#15 1080p H.264 · WEB-DL · GRP16 · 16.0 GB") {
		t.Fatalf("version names = %q, %q, %q", got.MediaSources[1].Name, got.MediaSources[2].Name, got.MediaSources[15].Name)
	}
	if len(got.Trickplay) > 0 && got.Trickplay[got.MediaSources[3].ID] == nil {
		t.Fatal("trickplay not keyed by release version")
	}

	pick := got.MediaSources[3] // #3 = Release04
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+pick.ID+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": pick.ID}))
	if len(info.MediaSources) != 1 {
		t.Fatalf("playback info = %+v", info)
	}
	src := info.MediaSources[0]
	if src.ID != pick.ID || src.Name != "Movie.2024.1080p.WEB-DL.H264-GRP04" {
		t.Fatalf("source = %s %q, want %s Release04", src.ID, src.Name, pick.ID)
	}
	if res, _ := resolved["result"].(map[string]any); res == nil || res["guid"] != "g4" {
		t.Fatalf("resolved %v", resolved)
	}
	if src.RunTimeTicks == nil || *src.RunTimeTicks != 5400*10_000_000 || !strings.Contains(src.TranscodingURL, "MediaSourceId="+pick.ID) {
		t.Fatalf("source runtime %v url %s", src.RunTimeTicks, src.TranscodingURL)
	}
	hasSub := false
	for _, st := range src.MediaStreams {
		if st.Type == "Video" && st.VideoRangeType != "HDR10" {
			t.Fatalf("video range = %q", st.VideoRangeType)
		}
		hasSub = hasSub || (st.Type == "Subtitle" && st.Index == 2)
	}
	if !hasSub {
		t.Fatalf("streams = %+v", src.MediaStreams)
	}

	// The automatic version is a separate play session (prequeue).
	auto := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": got.MediaSources[0].ID}))
	if len(auto.MediaSources) != 1 || auto.MediaSources[0].ID == pick.ID || auto.PlaySessionID == info.PlaySessionID {
		t.Fatalf("auto playback info = %+v", auto)
	}
}

// Release Versions sets how many releases are offered; 0 offers none and
// does not search for them.
func TestReleaseVersionsSetting(t *testing.T) {
	for _, tc := range []struct{ count, want int }{{0, 1}, {3, 4}} { // 1: the item's own source
		h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: tc.count})
		installFakePlayback(t, h)
		searches := 0
		h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
			searches++
			var results []map[string]any
			for i := 1; i <= 10; i++ {
				results = append(results, map[string]any{"title": fmt.Sprintf("Movie.2024.1080p.WEB.H264-G%02d", i), "guid": fmt.Sprintf("g%d", i)})
			}
			writeJSON(w, http.StatusOK, results)
		}
		tok := h.login("Living Room", "alice-pass")
		item := movieID(t, h, tok)
		got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
		if len(got.MediaSources) != tc.want {
			t.Fatalf("count %d: versions = %d, want %d", tc.count, len(got.MediaSources), tc.want)
		}
		if tc.count == 0 && searches != 0 {
			t.Fatalf("count 0 searched releases %d times", searches)
		}
	}
}
