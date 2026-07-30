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

func TestStartHLSSessionDirectCastProfilesAreNotStableCastTimeline(t *testing.T) {
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
			if body.StableCastTimeline {
				t.Fatalf("stableCastTimeline = true, want false for direct cast profile")
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

func TestDirectCastHDRTranscodingArgsUseRemuxFMP4WithoutLegacyCastForcing(t *testing.T) {
	args, logs := runCastArgPlanTest(t, &HLSSession{
		ID:             "direct-cast",
		Path:           "movie.mkv",
		OriginalPath:   "movie.mkv",
		OutputDir:      t.TempDir(),
		HasHDR:         true,
		CastMode:       true,
		PlaybackTarget: "cast-direct",
		ProbeData: &UnifiedProbeResult{
			Duration:           120,
			VideoCodec:         "hevc",
			VideoPixFmt:        "yuv420p10le",
			VideoProfile:       "Main 10",
			AudioStreams:       []audioStreamInfo{{Index: 1, Codec: "aac"}},
			HasCompatibleAudio: true,
		},
	}, false)

	if strings.Contains(logs, "session direct-cast: cast stable timeline requires deterministic H.264 segments") {
		t.Fatalf("direct cast produced legacy stable timeline transcode log; logs=%s", logs)
	}
	if !argPair(args, "-c:v", "copy") {
		t.Fatalf("direct cast args did not copy HEVC video; args=%v", args)
	}
	if !argPair(args, "-hls_segment_type", "fmp4") {
		t.Fatalf("direct cast args did not use fMP4 segments; args=%v", args)
	}
	if argPair(args, "-hls_segment_type", "mpegts") {
		t.Fatalf("direct cast args used legacy MPEG-TS segments; args=%v", args)
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
