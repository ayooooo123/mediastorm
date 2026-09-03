package handlers

import (
	"strings"
	"testing"
)

// A Cast receiver reads the playlist and the VTT and nothing else: our X-Subtitle-Start-Offset
// header is invisible to it. Without an in-band map it assumes WebVTT zero is TS zero, and the
// muxer's clock starts at 1.4s, so every cue lands 1.4s early on the screen.
func TestSubtitleResponseStatesItsTimestampBase(t *testing.T) {
	const cues = "WEBVTT\n\n00:08.989 --> 00:10.574\nWhat's up, guys?\n"

	mapped := withWebVTTTimestampMap(cues, mpegtsDefaultPreloadSeconds)
	want := "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:126000\n\n00:08.989 --> 00:10.574\nWhat's up, guys?\n"
	if mapped != want {
		t.Fatalf("expected the 1.4s preload stated as 126000 ticks:\n got %q\nwant %q", mapped, want)
	}

	// A stable cast timeline starts a whole run of segments in, and the same correction has to
	// carry that too, or the drift is seconds rather than milliseconds.
	stable := withWebVTTTimestampMap(cues, mpegtsDefaultPreloadSeconds+10*hlsSegmentDuration)
	if want := "MPEGTS:1926000"; !strings.Contains(stable, want) {
		t.Fatalf("stable-timeline base not carried; wanted %s in %q", want, stable)
	}
}

func TestSubtitleResponseLeavesAlreadyAlignedContentAlone(t *testing.T) {
	// The web path pins the video to a zero origin, so its VTT needs no correction at all.
	zeroBased := "WEBVTT\n\n00:01.000 --> 00:02.000\nhello\n"
	if got := withWebVTTTimestampMap(zeroBased, 0); got != zeroBased {
		t.Fatalf("a zero-origin session must not be rewritten; got %q", got)
	}

	already := "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:900000\n\n00:01.000 --> 00:02.000\nhi\n"
	if got := withWebVTTTimestampMap(already, mpegtsDefaultPreloadSeconds); got != already {
		t.Fatalf("an existing map must win; got %q", got)
	}

	// Extraction has produced nothing parseable yet: there is no header to attach a map to.
	if got := withWebVTTTimestampMap("", mpegtsDefaultPreloadSeconds); got != "" {
		t.Fatalf("empty content must be left as is; got %q", got)
	}
}

func TestClampNegativeSyncedWebVTTCueStarts(t *testing.T) {
	input := "WEBVTT\n\n00:-2.-50 --> 00:00.510\noverlap\n\n00:00.590 --> 00:02.670 align:start\nnext\n"
	want := "WEBVTT\n\n00:00.000 --> 00:00.510\noverlap\n\n00:00.590 --> 00:02.670 align:start\nnext\n"
	if got := clampNegativeSyncedWebVTTCueStarts(input); got != want {
		t.Fatalf("negative spanning cue was not clamped without shifting later cues:\n got %q\nwant %q", got, want)
	}

	valid := "WEBVTT\n\n00:00.590 --> 00:02.670\nhello\n"
	if got := clampNegativeSyncedWebVTTCueStarts(valid); got != valid {
		t.Fatalf("valid VTT must remain unchanged; got %q", got)
	}
}
