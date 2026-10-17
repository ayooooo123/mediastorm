package jellyfinbridge

import (
	"net/http"
	"reflect"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

func TestSessionsReportPlayMethodAndTranscodingInfo(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	plan := StreamPlan{SourceVideoCodec: "h264", VideoCodec: "h264", VideoCopied: true, Encoder: "copy",
		SourceAudioCodec: "ac3", AudioCodec: "aac", AudioChannels: 2, CompletionPercentage: 42.5}
	h.srv.deps.StreamPlan = func(id string) (StreamPlan, bool) { return plan, id == "abc1" }
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	sessions := func() dto.SessionInfoDto {
		t.Helper()
		list := decode[[]dto.SessionInfoDto](t, h.do(http.MethodGet, "/Sessions?deviceId=x", tok, nil))
		if len(list) != 1 {
			t.Fatalf("sessions = %d", len(list))
		}
		return list[0]
	}
	if got := sessions(); got.NowPlayingItem != nil || got.TranscodingInfo != nil || got.PlayState.RepeatMode != "RepeatNone" {
		t.Fatalf("idle session = %+v", got)
	}

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{}))
	h.do(http.MethodGet, info.MediaSources[0].TranscodingURL, "", nil)
	h.do(http.MethodPost, "/Sessions/Playing", tok, map[string]any{
		"ItemId": item, "PlaySessionId": info.PlaySessionID, "PlayMethod": "Transcode", "PositionTicks": 50000000, "CanSeek": true,
	})

	got := sessions()
	if got.NowPlayingItem == nil || got.NowPlayingItem.ID != item {
		t.Fatalf("now playing = %+v", got.NowPlayingItem)
	}
	if got.PlayState.PlayMethod != "Transcode" || got.PlayState.PositionTicks == nil || *got.PlayState.PositionTicks != 50000000 {
		t.Fatalf("play state = %+v", got.PlayState)
	}
	// Video copied, audio encoded: jellyfin-web shows "Direct streaming".
	ti := got.TranscodingInfo
	if ti == nil || !ti.IsVideoDirect || ti.IsAudioDirect || ti.VideoCodec != "h264" || ti.AudioCodec != "aac" ||
		ti.AudioChannels == nil || *ti.AudioChannels != 2 || !reflect.DeepEqual(ti.TranscodeReasons, []string{"AudioCodecNotSupported"}) {
		t.Fatalf("transcoding info = %+v", ti)
	}

	if ti.CompletionPercentage == nil || *ti.CompletionPercentage != 42.5 {
		t.Fatalf("completion = %v", ti.CompletionPercentage)
	}

	// Stopping clears it.
	h.do(http.MethodPost, "/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": item, "PlaySessionId": info.PlaySessionID, "PositionTicks": 0})
	if got := sessions(); got.NowPlayingItem != nil || got.TranscodingInfo != nil {
		t.Fatalf("session after stop = %+v", got)
	}
}

func TestTranscodeReasons(t *testing.T) {
	cases := []struct {
		name string
		plan StreamPlan
		want []string
	}{
		{"remux", StreamPlan{SourceVideoCodec: "h264", VideoCopied: true, SourceAudioCodec: "aac", AudioCopied: true}, []string{"ContainerNotSupported"}},
		{"hevc", StreamPlan{SourceVideoCodec: "hevc", SourceAudioCodec: "eac3"}, []string{"VideoCodecNotSupported", "AudioCodecNotSupported"}},
		{"hdr", StreamPlan{SourceVideoCodec: "hevc", SourceHDR: true, SourceAudioCodec: "aac", AudioCopied: true}, []string{"VideoRangeTypeNotSupported"}},
		{"10-bit h264", StreamPlan{SourceVideoCodec: "h264", SourcePixFmt: "yuv420p10le", SourceAudioCodec: "aac", AudioCopied: true}, []string{"VideoBitDepthNotSupported"}},
	}
	for _, tc := range cases {
		if got := transcodeReasons(tc.plan); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	hw := transcodingInfo(StreamPlan{VideoCodec: "h264", Encoder: "nvenc", HardwareEncode: true, SourceVideoCodec: "hevc"})
	if hw.HardwareAccelerationType != "nvenc" || hw.IsVideoDirect {
		t.Errorf("hardware encode = %+v", hw)
	}
}
