package jellyfinbridge

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/utils/parsett"
)

func TestReleaseLabelFromParsedName(t *testing.T) {
	title := "Movie.2024.German.DL.2160p.UHD.BluRay.REMUX.DV.HDR10Plus.HEVC.TrueHD.Atmos.7.1-FGT"
	parsed, _ := parsett.ParseTitle(title)
	got := releaseLabel(1, models.NZBResult{Title: title, Indexer: "nzbgeek", SizeBytes: 60 << 30}, parsed)
	for _, want := range []string{"#1 2160p HEVC DV HDR10+", "BluRay REMUX", "TrueHD", "7.1", "DE", "FGT", "60.0 GB"} {
		if !strings.Contains(got, want) {
			t.Errorf("label %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "nzbgeek") {
		t.Errorf("label %q names the indexer", got)
	}
	if got := releaseLabel(2, models.NZBResult{Title: "weird"}, nil); got != "#2 weird" {
		t.Errorf("unparsed label = %q", got)
	}
}

func TestTitleMayCopy(t *testing.T) {
	h264Only := &hlsClientCaps{TSVideo: []string{"h264"}}
	hdrBrowser := &hlsClientCaps{TSVideo: []string{"h264"}, FMP4Video: []string{"h264", "hevc"}, VideoRanges: map[string][]string{"hevc": {"SDR", "HDR10", "HLG"}}}
	for _, tc := range []struct {
		caps  *hlsClientCaps
		title string
		want  bool
	}{
		{h264Only, "Movie.2024.1080p.WEB.H264-GRP", true},
		{h264Only, "Movie.2024.1080p.WEB.H265-GRP", false},
		{hdrBrowser, "Movie.2024.2160p.WEB.H265-GRP", true},
		{hdrBrowser, "Movie.2024.2160p.HDR.WEB.H265-GRP", true},
		{hdrBrowser, "Movie.2024.2160p.DV.WEB.H265-GRP", false}, // DV without base layer
		{hdrBrowser, "Movie.2024.2160p.DV.HDR.WEB.H265-GRP", true},
		{hdrBrowser, "Movie.2024.1080p.WEB.AV1-GRP", false},
	} {
		parsed, _ := parsett.ParseTitle(tc.title)
		if got := titleMayCopy(tc.caps, parsed); got != tc.want {
			t.Errorf("titleMayCopy(%s) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// The automatic version skips mediastorm's HEVC pick for an H.264-only
// client and plays the best H.264 release instead.
func TestAutoAvoidsVideoTranscoding(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{AutoAvoidVideoTranscoding: true, CopyHEVC: true, CopyHDR: true, CopyDolbyVision: true})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"title": "Movie.2024.2160p.WEB.H265-TOP", "guid": "g1"},
			{"title": "Movie.2024.2160p.WEB.H265-SECOND", "guid": "g2"},
			{"title": "Movie.2024.1080p.WEB.H264-AVC", "guid": "g3"},
		})
	}
	var resolves int32
	h.srv.deps.Handlers.PlaybackResolve = func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&resolves, 1)
		writeJSON(w, http.StatusOK, map[string]any{"queueId": 0, "webdavPath": "/webdav/avc.mkv"})
	}
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		codec := "hevc"
		if r.URL.Query().Get("path") == "/webdav/avc.mkv" {
			codec = "h264"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"durationSeconds": 100,
			"videoStreams":    []map[string]any{{"index": 0, "codecName": codec, "pixFmt": "yuv420p", "profile": "Main"}},
			"audioStreams":    []map[string]any{{"index": 1, "codecName": "aac"}},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	profile := map[string]any{"TranscodingProfiles": []map[string]any{{"Container": "ts", "Type": "Video", "Protocol": "hls", "VideoCodec": "h264", "AudioCodec": "aac"}}}

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"DeviceProfile": profile}))
	if len(info.MediaSources) != 1 {
		t.Fatalf("playback info = %+v", info)
	}
	src := info.MediaSources[0]
	if src.Name != "Movie.2024.1080p.WEB.H264-AVC" {
		t.Fatalf("auto played %q, want the H.264 release", src.Name)
	}
	if src.ID == "" || !strings.Contains(src.TranscodingURL, "MediaSourceId="+src.ID) {
		t.Fatalf("source %+v", src)
	}
	// The H.265 releases are ruled out by name; only the H.264 one is resolved.
	if n := atomic.LoadInt32(&resolves); n != 1 {
		t.Fatalf("resolves = %d, want 1", n)
	}
}

// Without declared caps, or with the setting off, Auto stays mediastorm's pick.
func TestAutoKeepsPickWithoutCaps(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{AutoAvoidVideoTranscoding: true, CopyHEVC: true})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		t.Error("searched releases without client caps")
		writeJSON(w, http.StatusOK, []map[string]any{})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{}))
	if len(info.MediaSources) != 1 {
		t.Fatalf("playback info = %+v", info)
	}
}

// Chapters of a probed stream appear on the item.
func TestChaptersFromProbedStream(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"videoStreams": []map[string]any{{"index": 0, "codecName": "h264"}},
			"chapters":     []map[string]any{{"title": "Intro", "start": 0}, {"title": "", "start": 90.5}},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{})
	got := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	if len(got.Chapters) != 2 {
		t.Fatalf("chapters = %+v", got.Chapters)
	}
	second := got.Chapters[1].(map[string]any)
	if second["Name"] != "Kapitel 2" || second["StartPositionTicks"].(float64) != 905_000_000 {
		t.Fatalf("chapter = %+v", second)
	}
}
