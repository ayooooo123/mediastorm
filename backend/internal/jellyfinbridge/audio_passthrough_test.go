package jellyfinbridge

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/services/playback"
)

// edgeProfile is a trimmed jellyfin-web DeviceProfile as Edge on an HDR
// display sends it: HEVC only in fMP4, AC-3/E-AC-3 in both containers.
func edgeProfile(maxChannels string) map[string]any {
	return map[string]any{
		"TranscodingProfiles": []map[string]any{
			{"Container": "mp4", "Type": "Video", "Protocol": "hls", "VideoCodec": "hevc,h264,av1", "AudioCodec": "aac,ac3,eac3", "MaxAudioChannels": maxChannels},
			{"Container": "ts", "Type": "Video", "Protocol": "hls", "VideoCodec": "h264", "AudioCodec": "aac,mp3,ac3,eac3", "MaxAudioChannels": maxChannels},
			{"Container": "mp3", "Type": "Audio", "Protocol": "http", "AudioCodec": "mp3"},
		},
		"CodecProfiles": []map[string]any{
			{"Type": "Video", "Codec": "hevc", "Conditions": []map[string]any{
				{"Condition": "EqualsAny", "Property": "VideoProfile", "Value": "main|main 10"},
				{"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": "SDR|HDR10|HDR10Plus|HLG"},
			}},
			{"Type": "Video", "Codec": "h264", "Conditions": []map[string]any{
				{"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": "SDR"},
			}},
		},
	}
}

func TestCapsFromDeviceProfile(t *testing.T) {
	raw, _ := json.Marshal(edgeProfile("6"))
	caps := capsFromProfile(raw)
	if caps == nil {
		t.Fatal("no caps")
	}
	want := hlsClientCaps{
		TSVideo: []string{"h264"}, TSAudio: []string{"aac", "mp3", "ac3", "eac3"},
		FMP4Video: []string{"hevc", "h264", "av1"}, FMP4Audio: []string{"aac", "ac3", "eac3"},
		VideoRanges:      map[string][]string{"hevc": {"SDR", "HDR10", "HDR10Plus", "HLG"}, "h264": {"SDR"}},
		MaxAudioChannels: 6,
	}
	if !reflect.DeepEqual(*caps, want) {
		t.Fatalf("caps = %+v", *caps)
	}

	caps.restrict(true, false, false, 8)
	if !reflect.DeepEqual(caps.VideoRanges["hevc"], []string{"SDR"}) {
		t.Fatalf("HDR off kept ranges %v", caps.VideoRanges["hevc"])
	}
	if !reflect.DeepEqual(caps.TSAudio, []string{"aac"}) || !reflect.DeepEqual(caps.FMP4Audio, []string{"aac"}) {
		t.Fatalf("8 channels > 6 kept audio %v %v", caps.TSAudio, caps.FMP4Audio)
	}
	caps.restrict(false, true, true, 2)
	if containsFold(caps.FMP4Video, "hevc") || containsFold(caps.FMP4Video, "av1") || !containsFold(caps.FMP4Video, "h264") {
		t.Fatalf("HEVC off kept %v", caps.FMP4Video)
	}
	if capsFromProfile(json.RawMessage(`{"TranscodingProfiles":[]}`)) != nil {
		t.Fatal("caps without HLS profiles")
	}
}

func TestDVRangeType(t *testing.T) {
	cases := []struct {
		status playback.PrequeueStatusResponse
		want   string
	}{
		{playback.PrequeueStatusResponse{DolbyVisionProfile: "dvhe.05.06"}, "DOVI"},
		{playback.PrequeueStatusResponse{DolbyVisionProfile: "dvhe.08.06", DolbyVisionConfiguration: &models.DolbyVisionConfiguration{BLSignalCompatibilityID: 1}}, "DOVIWithHDR10"},
		{playback.PrequeueStatusResponse{DolbyVisionProfile: "dvhe.08.06", HasHDR10: true}, "DOVIWithHDR10"},
		{playback.PrequeueStatusResponse{DolbyVisionProfile: "dvhe.08.04", DolbyVisionConfiguration: &models.DolbyVisionConfiguration{BLSignalCompatibilityID: 4}}, "DOVIWithHLG"},
		{playback.PrequeueStatusResponse{DolbyVisionProfile: "dvhe.07.06"}, "DOVIWithEL"},
	}
	for _, tc := range cases {
		if got := dvRangeType(&tc.status); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.status.DolbyVisionProfile, got, tc.want)
		}
	}
}

func TestHLSStartCarriesClientCaps(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{CopyHEVC: true, CopyHDR: true, CopyDolbyVision: true})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"audioStreams": []map[string]any{{"index": 1, "codecName": "aac", "channels": 2}}})
	}
	var startQuery url.Values
	start := h.srv.deps.Handlers.HLSStart
	h.srv.deps.Handlers.HLSStart = func(w http.ResponseWriter, r *http.Request) {
		startQuery = r.URL.Query()
		start(w, r)
	}
	h.srv.deps.StreamPlan = func(string) (StreamPlan, bool) { return StreamPlan{VideoCopied: true}, true }
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"DeviceProfile": edgeProfile("6")}))
	h.do(http.MethodGet, info.MediaSources[0].TranscodingURL, "", nil)
	var caps hlsClientCaps
	if err := json.Unmarshal([]byte(startQuery.Get("clientCaps")), &caps); err != nil || !containsFold(caps.FMP4Video, "hevc") || len(caps.VideoRanges["hevc"]) != 4 {
		t.Fatalf("clientCaps = %q (%v)", startQuery.Get("clientCaps"), err)
	}

	// A failed copy makes the next attempt on this device transcode.
	h.do(http.MethodPost, "/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": item, "PlaySessionId": info.PlaySessionID, "PositionTicks": 0, "Failed": true})
	info = decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"DeviceProfile": edgeProfile("6")}))
	startQuery = nil
	h.do(http.MethodGet, info.MediaSources[0].TranscodingURL, "", nil)
	if startQuery == nil || startQuery.Get("clientCaps") != `{"noVideoCopy":true}` {
		t.Fatalf("after failure clientCaps = %q", startQuery.Get("clientCaps"))
	}
}

func TestDisableTranscodingForcesCopy(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{DisableVideoTranscoding: true, DisableAudioTranscoding: true})
	sess := &playSession{copyFailed: true}
	var caps hlsClientCaps
	if err := json.Unmarshal([]byte(h.srv.clientCapsParam(sess, nil, -1)), &caps); err != nil || !caps.ForceVideoCopy || !caps.ForceAudioCopy || caps.NoVideoCopy {
		t.Fatalf("caps with transcoding disabled = %+v (%v)", caps, err)
	}

	// Audio only: the video may still fall back to a transcode after a failure.
	audioOnly := newHarness(t, config.JellyfinBridgeSettings{DisableAudioTranscoding: true})
	caps = hlsClientCaps{}
	if err := json.Unmarshal([]byte(audioOnly.srv.clientCapsParam(&playSession{copyFailed: true}, nil, -1)), &caps); err != nil || caps.ForceVideoCopy || !caps.ForceAudioCopy || !caps.NoVideoCopy {
		t.Fatalf("audio-only caps = %+v (%v)", caps, err)
	}
	if audioOnly.srv.liveTarget() != "web" {
		t.Fatal("live TV passed through with only audio transcoding disabled")
	}
	if h.srv.liveTarget() != "native" {
		t.Fatal("live TV still transcoded")
	}
	normal := newHarness(t, config.JellyfinBridgeSettings{})
	if normal.srv.liveTarget() != "web" || normal.srv.clientCapsParam(&playSession{}, nil, -1) != "" {
		t.Fatal("defaults changed")
	}
}

func TestSettingsApplyWithoutRestart(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{Port: 8096, ServerName: "test"})
	current := config.JellyfinBridgeSettings{Port: 9999, ServerName: "renamed"}
	h.srv.deps.Settings = func() (config.JellyfinBridgeSettings, error) { return current, nil }
	if h.srv.settings().DisableVideoTranscoding || h.srv.liveTarget() != "web" {
		t.Fatal("transcoding disabled before the setting changed")
	}
	current.DisableVideoTranscoding = true
	h.srv.live.loaded = time.Time{} // skip the re-read interval
	if !h.srv.settings().DisableVideoTranscoding || h.srv.liveTarget() != "native" {
		t.Fatal("Disable Transcoding needs a restart")
	}
	if got := h.srv.settings(); got.Port != 8096 || got.ServerName != "renamed" {
		t.Fatalf("listener settings followed the file: %+v", got)
	}
}
