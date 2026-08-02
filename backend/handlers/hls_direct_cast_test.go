package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/services/castcaps"
	"novastream/services/streaming"
)

type directCastTestProvider struct {
	directURL string
}

func (p directCastTestProvider) Stream(context.Context, streaming.Request) (*streaming.Response, error) {
	return &streaming.Response{Body: io.NopCloser(strings.NewReader("video"))}, nil
}

func (p directCastTestProvider) GetDirectURL(context.Context, string) (string, error) {
	return p.directURL, nil
}

func TestStartHLSSessionDirectCastWithoutProbeFallsBackToStableTimeline(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("not a real media file"), 0644); err != nil {
		t.Fatalf("write input fixture: %v", err)
	}

	tests := []struct {
		name  string
		query url.Values
	}{
		{
			name: "target cast-direct",
			query: url.Values{
				"path":         {inputPath},
				"cast":         {"true"},
				"target":       {"cast-direct"},
				"durationHint": {"120"},
			},
		},
		{
			name: "castProfile direct",
			query: url.Values{
				"path":         {inputPath},
				"cast":         {"true"},
				"target":       {"web"},
				"castProfile":  {"direct"},
				"durationHint": {"120"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewVideoHandlerWithProvider(true, "/usr/bin/true", "/definitely-missing-ffprobe", t.TempDir(), directCastTestProvider{directURL: inputPath})
			defer handler.hlsManager.Shutdown()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/video/hls/start?"+tc.query.Encode(), nil)
			handler.StartHLSSession(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var body struct {
				StableCastTimeline bool `json:"stableCastTimeline"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !body.StableCastTimeline {
				t.Fatalf("stableCastTimeline = false, want true when direct cast safety cannot be verified")
			}
		})
	}
}

func TestStartHLSSessionCompatibilityCastRemainsStableTimeline(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("not a real media file"), 0644); err != nil {
		t.Fatalf("write input fixture: %v", err)
	}

	handler := NewVideoHandlerWithProvider(true, "/usr/bin/true", "/definitely-missing-ffprobe", t.TempDir(), directCastTestProvider{directURL: inputPath})
	defer handler.hlsManager.Shutdown()

	query := url.Values{
		"path":         {inputPath},
		"cast":         {"true"},
		"forceAAC":     {"true"},
		"target":       {"web"},
		"castProfile":  {"compatibility"},
		"durationHint": {"120"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/video/hls/start?"+query.Encode(), nil)
	handler.StartHLSSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		StableCastTimeline bool `json:"stableCastTimeline"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.StableCastTimeline {
		t.Fatalf("stableCastTimeline = false, want true for compatibility cast profile")
	}
}

func TestDirectCastH264RemuxesToMpegTSWithoutLegacyCastForcing(t *testing.T) {
	args, logs := runCastArgPlanTest(t, &HLSSession{
		ID:             "direct-cast",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		CastMode:       true,
		DirectCastMode: true,
		PlaybackTarget: "cast-direct",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "h264",
			VideoPixFmt:        "yuv420p",
			VideoProfile:       "High",
			VideoWidth:         1920,
			VideoHeight:        1080,
			VideoLevel:         41,
			AvgFrameRate:       "24000/1001",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, false)

	if strings.Contains(logs, "session direct-cast: cast stable timeline requires deterministic H.264 segments") {
		t.Fatalf("direct cast produced legacy stable timeline transcode log; logs=%s", logs)
	}
	if !argPair(args, "-c:v", "copy") {
		t.Fatalf("direct cast args did not copy H.264 video; args=%v", args)
	}
	// Receivers built into TVs accept an fMP4 HLS load and then never start
	// playing; H.264 remuxes into MPEG-TS with no re-encode, so direct Cast
	// keeps the copy path in the container every receiver understands.
	if !argPair(args, "-hls_segment_type", "mpegts") {
		t.Fatalf("direct cast args did not use MPEG-TS segments; args=%v", args)
	}
	if argPair(args, "-hls_segment_type", "fmp4") {
		t.Fatalf("direct cast args used fMP4 segments; args=%v", args)
	}
}

// A gen2 Chromecast cannot decode HEVC at any resolution, and every receiver
// tested so far stalls silently on an fMP4 HLS load. An unprobed receiver
// therefore gets the compatibility transcode rather than an HEVC copy.
func TestDirectCastHEVCFallsBackToCompatibilityWithoutCapabilityProof(t *testing.T) {
	args, logs := runCastArgPlanTest(t, &HLSSession{
		ID:             "direct-cast-hevc",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		HasHDR:         true,
		CastMode:       true,
		DirectCastMode: true,
		PlaybackTarget: "cast-direct",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "hevc",
			VideoPixFmt:        "yuv420p10le",
			VideoProfile:       "Main 10",
			VideoWidth:         3840,
			VideoHeight:        2160,
			VideoLevel:         153,
			AvgFrameRate:       "24000/1001",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, false)

	if !strings.Contains(logs, "outside the receiver copy envelope") {
		t.Fatalf("HEVC direct cast did not fall back to compatibility; logs=%s", logs)
	}
	if argPair(args, "-c:v", "copy") {
		t.Fatalf("HEVC direct cast copied 4K video a receiver cannot decode; args=%v", args)
	}
	if !argPair(args, "-hls_segment_type", "mpegts") {
		t.Fatalf("compatibility fallback must use MPEG-TS; args=%v", args)
	}
}

func TestDirectCast1080pH264StillCopies(t *testing.T) {
	args, logs := runCastArgPlanTest(t, &HLSSession{
		ID:             "direct-cast-1080p",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		CastMode:       true,
		DirectCastMode: true,
		PlaybackTarget: "cast-direct",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "h264",
			VideoPixFmt:        "yuv420p",
			VideoProfile:       "High",
			VideoWidth:         1920,
			VideoHeight:        1080,
			VideoLevel:         41,
			AvgFrameRate:       "24000/1001",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, false)

	if strings.Contains(logs, "outside the receiver copy envelope") {
		t.Fatalf("in-envelope H.264 must keep the copy path; logs=%s", logs)
	}
	if !argPair(args, "-c:v", "copy") {
		t.Fatalf("1080p H.264 direct cast did not copy video; args=%v", args)
	}
	if !argPair(args, "-hls_segment_type", "mpegts") {
		t.Fatalf("direct cast copy must remux into MPEG-TS; args=%v", args)
	}
}

func TestDirectCastNonAACAudioReEncodesAudioOnlyInMpegTS(t *testing.T) {
	args, logs := runCastArgPlanTest(t, &HLSSession{
		ID:             "direct-cast-aac",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		CastMode:       true,
		DirectCastMode: true,
		PlaybackTarget: "cast-direct",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "h264",
			VideoPixFmt:        "yuv420p",
			VideoProfile:       "High",
			VideoWidth:         1920,
			VideoHeight:        1080,
			VideoLevel:         41,
			AvgFrameRate:       "24000/1001",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "eac3"}},
			HasCompatibleAudio: true,
		},
	}, false)

	if strings.Contains(logs, "cast stable timeline requires deterministic H.264 segments") {
		t.Fatalf("direct forceAAC cast unexpectedly used stable timeline; logs=%s", logs)
	}
	if !argPair(args, "-c:v", "copy") {
		t.Fatalf("direct forceAAC cast did not copy video; args=%v", args)
	}
	if !argPair(args, "-c:a:0", "aac") {
		t.Fatalf("direct cast did not re-encode E-AC-3 audio the receiver may not decode; args=%v", args)
	}
	if !argPair(args, "-ac:a:0", "2") || !argPair(args, "-profile:a:0", "aac_low") {
		t.Fatalf("direct cast audio must be stereo AAC-LC; receivers reject multichannel AAC; args=%v", args)
	}
	if argPair(args, "-c:a:1", "copy") {
		t.Fatalf("direct cast must not carry a second, undecodable audio track; args=%v", args)
	}
	if !strings.Contains(logs, "direct Cast audio is not AAC") {
		t.Fatalf("direct cast did not log the audio-only re-encode; logs=%s", logs)
	}
	if !argPair(args, "-hls_segment_type", "mpegts") {
		t.Fatalf("direct cast should remux into MPEG-TS; args=%v", args)
	}
}

func TestDirectCastIncompatibleVideoFallsBackToCompatibilityTranscode(t *testing.T) {
	args, logs := runCastArgPlanTest(t, &HLSSession{
		ID:             "direct-cast-incompatible",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		CastMode:       true,
		DirectCastMode: true,
		PlaybackTarget: "cast-direct",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "mpeg4",
			VideoPixFmt:        "yuv420p",
			VideoProfile:       "Simple Profile",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, false)

	if !strings.Contains(logs, "session direct-cast-incompatible: cast direct video is outside the receiver copy envelope") {
		t.Fatalf("incompatible direct cast did not log compatibility fallback; logs=%s", logs)
	}
	if argPair(args, "-c:v", "copy") {
		t.Fatalf("incompatible direct cast copied video instead of transcoding; args=%v", args)
	}
	if !argPair(args, "-hls_segment_type", "mpegts") {
		t.Fatalf("incompatible direct cast did not use MPEG-TS compatibility segments; args=%v", args)
	}
}

func TestCompatibilityCastHDRTranscodingArgsKeepLegacyStableMPEGTSPath(t *testing.T) {
	args, logs := runCastArgPlanTest(t, &HLSSession{
		ID:             "compat-cast",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		HasHDR:         true,
		CastMode:       true,
		PlaybackTarget: "web",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "hevc",
			VideoPixFmt:        "yuv420p10le",
			VideoProfile:       "Main 10",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, true)

	if !strings.Contains(logs, "session compat-cast: cast stable timeline requires deterministic H.264 segments") {
		t.Fatalf("compatibility cast did not produce legacy stable timeline transcode log; logs=%s", logs)
	}
	if !argPair(args, "-hls_segment_type", "mpegts") {
		t.Fatalf("compatibility cast args did not use MPEG-TS segments; args=%v", args)
	}
	if argPair(args, "-c:v", "copy") {
		t.Fatalf("compatibility cast unexpectedly copied video instead of legacy transcode; args=%v", args)
	}
}

func runCastArgPlanTest(t *testing.T, session *HLSSession, forceAAC bool) ([]string, string) {
	t.Helper()

	capturePath := filepath.Join(t.TempDir(), "ffmpeg-args.txt")
	ffmpegPath := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	script := "#!/bin/sh\nprintf '%s\n' \"$@\" > \"$CAPTURE_FFMPEG_ARGS\"\nexit 0\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	t.Setenv("CAPTURE_FFMPEG_ARGS", capturePath)

	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("not a real media file"), 0644); err != nil {
		t.Fatalf("write input fixture: %v", err)
	}
	manager := NewHLSManager(t.TempDir(), ffmpegPath, "", directCastTestProvider{directURL: inputPath})
	defer manager.Shutdown()

	if err := os.MkdirAll(session.OutputDir, 0755); err != nil {
		t.Fatalf("create session output dir: %v", err)
	}
	session.CreatedAt = time.Now()
	session.LastAccess = time.Now()
	session.LastSegmentRequest = time.Now()
	session.MinSegmentRequested = -1
	session.MaxSegmentRequested = -1
	session.LastPlaybackSegment = -1
	session.LastSegmentServed = -1
	session.EarliestBufferedSegment = -1
	session.FinalSegmentCount = -1

	var logBuf strings.Builder
	originalLogOutput := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(originalLogOutput) })

	if err := manager.startTranscoding(context.Background(), session, forceAAC); err != nil {
		t.Fatalf("startTranscoding returned error: %v", err)
	}

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read captured ffmpeg args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(args) == 1 && args[0] == "" {
		args = nil
	}
	return args, logBuf.String()
}

func argPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestCompatibilityCastTranscodesAt1080pByDefault(t *testing.T) {
	args, _ := runCastArgPlanTest(t, &HLSSession{
		ID:             "compat-cast-1080",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		CastMode:       true,
		PlaybackTarget: "web",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "h264",
			VideoPixFmt:        "yuv420p",
			VideoProfile:       "High",
			VideoWidth:         1920,
			VideoHeight:        1080,
			AvgFrameRate:       "24000/1001",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, true)

	filter := castFilterArg(args)
	if !strings.Contains(filter, "min(1920,iw)") || !strings.Contains(filter, "min(1080,ih)") {
		t.Fatalf("compatibility cast did not encode at 1080p; filter=%q args=%v", filter, args)
	}
}

func TestCompatibilityCastHonoursSlowLinkHeightCap(t *testing.T) {
	args, _ := runCastArgPlanTest(t, &HLSSession{
		ID:             "compat-cast-720",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		CastMode:       true,
		PlaybackTarget: "web",
		CastMaxHeight:  legacyCastHDMaxHeight,
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "h264",
			VideoPixFmt:        "yuv420p",
			VideoProfile:       "High",
			VideoWidth:         1920,
			VideoHeight:        1080,
			AvgFrameRate:       "24000/1001",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, true)

	filter := castFilterArg(args)
	if !strings.Contains(filter, "min(1280,iw)") || !strings.Contains(filter, "min(720,ih)") {
		t.Fatalf("slow-link cap did not drop the encode to 720p; filter=%q args=%v", filter, args)
	}
}

func castFilterArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-vf" {
			return args[i+1]
		}
	}
	return ""
}

// Capability widening must prove the exact thing it permits. VariantFMP4 is an
// H.264 asset: it says the container is accepted, nothing about HEVC decode.
func TestDirectCastCopyWideningRequiresMatchingProof(t *testing.T) {
	hevc4K := &UnifiedProbeResult{
		VideoCodec:   "hevc",
		VideoWidth:   3840,
		VideoHeight:  2160,
		VideoLevel:   153,
		AvgFrameRate: "24000/1001",
	}
	h264Above := &UnifiedProbeResult{
		VideoCodec:   "h264",
		VideoWidth:   3840,
		VideoHeight:  2160,
		VideoLevel:   51,
		AvgFrameRate: "24000/1001",
	}
	caps := func(verdict castcaps.Verdict, variants ...castcaps.Variant) *castcaps.Capabilities {
		c := &castcaps.Capabilities{Variants: map[castcaps.Variant]castcaps.Verdict{}}
		for _, v := range variants {
			c.Variants[v] = verdict
		}
		return c
	}
	proven := func(variants ...castcaps.Variant) *castcaps.Capabilities {
		return caps(castcaps.VerdictSupported, variants...)
	}
	assumed := func(variants ...castcaps.Variant) *castcaps.Capabilities {
		return caps(castcaps.VerdictAssumed, variants...)
	}

	for _, tc := range []struct {
		name  string
		probe *UnifiedProbeResult
		caps  *castcaps.Capabilities
		want  bool
	}{
		{"unidentified receiver refuses 4K HEVC", hevc4K, nil, false},
		{"identified receiver with no verdicts refuses 4K HEVC", hevc4K, caps(castcaps.VerdictUnknown), false},
		{"container proof alone refuses HEVC", hevc4K, proven(castcaps.VariantFMP4), false},
		{"HEVC proof without container refuses", hevc4K, proven(castcaps.VariantHEVCFMP4), false},
		{"both proofs allow HEVC copy", hevc4K, proven(castcaps.VariantFMP4, castcaps.VariantHEVCFMP4), true},
		// A model prior is worth one attempt: the cost of being wrong is a
		// session that falls back, not a session that silently never starts.
		{"model prior allows an HEVC copy attempt", hevc4K, assumed(castcaps.VariantFMP4, castcaps.VariantHEVCFMP4), true},
		{"container prior alone refuses HEVC", hevc4K, assumed(castcaps.VariantFMP4), false},
		{"an observed rejection outranks the container prior", hevc4K, func() *castcaps.Capabilities {
			c := assumed(castcaps.VariantFMP4)
			c.Variants[castcaps.VariantHEVCFMP4] = castcaps.VerdictRejected
			return c
		}(), false},
		{"no variant measures 4K H.264", h264Above, proven(castcaps.VariantFMP4, castcaps.VariantHEVCFMP4), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := canAttemptDirectCastCopyVideo(tc.probe, tc.caps); got != tc.want {
				t.Fatalf("canAttemptDirectCastCopyVideo = %v, want %v", got, tc.want)
			}
		})
	}
}
