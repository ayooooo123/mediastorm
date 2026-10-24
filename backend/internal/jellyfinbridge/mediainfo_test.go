package jellyfinbridge

import (
	"net/http"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

func TestPlaybackInfoCarriesProbedStreamDetails(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	probes := 0
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		probes++
		if r.URL.Query().Get("path") != "/webdav/movie.mkv" {
			t.Errorf("probe path = %q", r.URL.Query().Get("path"))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"formatBitRate": 8000000,
			"videoStreams": []map[string]any{{
				"index": 0, "codecName": "hevc", "width": 1920, "height": 1080, "bitRate": 7000000,
				"pixFmt": "yuv420p10le", "profile": "Main 10", "level": 120, "avgFrameRate": "24000/1001",
				"colorSpace": "bt709", "colorPrimaries": "bt709", "colorTransfer": "bt709",
			}},
			"audioStreams": []map[string]any{{
				"index": 1, "codecName": "aac", "channels": 6, "sampleRate": 48000, "bitRate": 384000, "channelLayout": "5.1(side)",
			}},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{}))
	src := info.MediaSources[0]
	if src.Bitrate == nil || *src.Bitrate != 8000000 {
		t.Fatalf("source bitrate = %v", src.Bitrate)
	}
	var video, audio *dto.MediaStream
	for i := range src.MediaStreams {
		switch src.MediaStreams[i].Type {
		case "Video":
			video = &src.MediaStreams[i]
		case "Audio":
			audio = &src.MediaStreams[i]
		}
	}
	if video == nil || video.Codec != "hevc" || video.Width == nil || *video.Width != 1920 || video.BitDepth == nil || *video.BitDepth != 10 ||
		video.AverageFrameRate == nil || *video.AverageFrameRate != 23.976 || video.Level == nil || *video.Level != 120 ||
		video.PixelFormat != "yuv420p10le" || video.DisplayTitle != "1080p HEVC SDR" {
		t.Fatalf("video stream = %+v", video)
	}
	if audio == nil || audio.Channels == nil || *audio.Channels != 6 || audio.SampleRate == nil || *audio.SampleRate != 48000 ||
		audio.BitRate == nil || *audio.BitRate != 384000 || audio.DisplayTitle != "ENG - AAC 5.1" {
		t.Fatalf("audio stream = %+v", audio)
	}

	// The play session keeps the probe.
	h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{})
	if probes != 1 {
		t.Fatalf("probes = %d, want 1 (cached per play session)", probes)
	}

	// A device profile without HEVC no longer direct plays an HEVC file.
	profile := map[string]any{"DirectPlayProfiles": []map[string]any{{"Type": "Video", "Container": "mkv", "VideoCodec": "h264", "AudioCodec": "aac"}}}
	native := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{"DeviceProfile": profile}))
	if native.MediaSources[0].SupportsDirectPlay {
		t.Fatal("HEVC offered for direct play to an H.264-only device profile")
	}
	profile["DirectPlayProfiles"].([]map[string]any)[0]["VideoCodec"] = "h264,hevc"
	native = decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{"DeviceProfile": profile}))
	if !native.MediaSources[0].SupportsDirectPlay {
		t.Fatal("HEVC-capable device profile not offered direct play")
	}
}

func TestParseFrameRateAndBitDepth(t *testing.T) {
	if got := parseFrameRate("30000/1001"); got != 29.97 {
		t.Errorf("30000/1001 = %v", got)
	}
	if got := parseFrameRate("0/0"); got != 0 {
		t.Errorf("0/0 = %v", got)
	}
	for pix, want := range map[string]int{"yuv420p": 8, "yuv420p10le": 10, "p010le": 10, "yuv444p12le": 12, "": 0} {
		if got := pixFmtBitDepth(pix); got != want {
			t.Errorf("%q = %d, want %d", pix, got, want)
		}
	}
}

// Direct streams often report no duration and metadata lacks many episode
// runtimes; progress then uses the probed length, also for reports that
// carry no play session.
func TestProgressUsesProbedDuration(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.PrequeueStatus = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{
			PrequeueID: "pq1", Status: playback.PrequeueStatusReady, StreamPath: "/webdav/movie.mkv",
			SelectedAudioTrack: -1, SelectedSubtitleTrack: -1,
		})
	}
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"durationSeconds": 1320})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{}))
	if rt := info.MediaSources[0].RunTimeTicks; rt == nil || *rt != 1320*ticksPerSecond {
		t.Fatalf("runtime = %v, want the probed 1320 s", rt)
	}
	h.do(http.MethodPost, "/Sessions/Playing/Progress", tok, map[string]any{"ItemId": item, "PlaySessionId": info.PlaySessionID, "PositionTicks": int64(660) * ticksPerSecond})
	h.do(http.MethodPost, "/Sessions/Playing/Progress", tok, map[string]any{"ItemId": item, "PositionTicks": int64(990) * ticksPerSecond})
	if got := h.history.progress; len(got) != 2 || got[0].Duration != 1320 || got[0].PercentWatched != 50 || got[1].Duration != 1320 {
		t.Fatalf("progress = %+v, want duration 1320 with and without the play session", got)
	}
}
