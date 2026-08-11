package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

func TestThumbnailTimesCapsLongVideos(t *testing.T) {
	interval, times := thumbnailTimes(3*60*60, 30)
	if len(times) > thumbnailMaxCount {
		t.Fatalf("expected capped thumbnails, got %d", len(times))
	}
	if interval < 90 {
		t.Fatalf("expected interval to increase for long video, got %d", interval)
	}
}

func TestThumbnailTimesShortVideo(t *testing.T) {
	interval, times := thumbnailTimes(90, 60)
	if interval != thumbnailDefaultIntervalSec {
		t.Fatalf("expected default interval, got %d", interval)
	}
	if len(times) == 0 {
		t.Fatal("expected at least one thumbnail for short VOD")
	}
}

func TestThumbnailGenerationTargetsPrioritizeChaptersThenProceedSequentially(t *testing.T) {
	interval, targets := thumbnailGenerationTargets(300, 60, []float64{180, 45, 120})
	if interval != 60 {
		t.Fatalf("interval = %d, want 60", interval)
	}
	wantTimes := []float64{50, 125, 185, 30, 90, 150, 210, 270}
	if len(targets) != len(wantTimes) {
		t.Fatalf("targets = %v, want %d items", targets, len(wantTimes))
	}
	for index, want := range wantTimes {
		if targets[index].TimeSec != want {
			t.Fatalf("target %d time = %.1f, want %.1f", index, targets[index].TimeSec, want)
		}
		if targets[index].Priority != (index < 3) {
			t.Fatalf("target %d priority = %t", index, targets[index].Priority)
		}
	}
}

func TestThumbnailGenerationTargetsMoveInsideChapterAndRejectInvalidTimes(t *testing.T) {
	_, targets := thumbnailGenerationTargets(180, 60, []float64{30, 30, -1, 179.5, 200})
	wantTimes := []float64{35, 30, 90, 150}
	if len(targets) != len(wantTimes) {
		t.Fatalf("targets = %v, want %d items", targets, len(wantTimes))
	}
	for index, want := range wantTimes {
		if targets[index].TimeSec != want {
			t.Fatalf("target %d time = %.1f, want %.1f", index, targets[index].TimeSec, want)
		}
	}
	if !targets[0].Priority {
		t.Fatal("chapter capture timestamps should retain priority")
	}
}

func TestThumbnailChapterCaptureTimesStayInsideChapterBoundaries(t *testing.T) {
	got := thumbnailChapterCaptureTimes(120, []float64{0, 0.4, 60})
	want := []float64{0.2, 5.4, 65}
	if len(got) != len(want) {
		t.Fatalf("capture times = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("capture %d = %.1f, want %.1f", index, got[index], want[index])
		}
	}
}

func TestThumbnailWorkerCountFromSetting(t *testing.T) {
	if got := thumbnailWorkerCountFromSetting(0); got != thumbnailDefaultWorkers {
		t.Fatalf("zero worker count = %d, want %d", got, thumbnailDefaultWorkers)
	}
	if got := thumbnailWorkerCountFromSetting(3); got != 3 {
		t.Fatalf("configured worker count = %d, want 3", got)
	}
	if got := thumbnailWorkerCountFromSetting(thumbnailMaxWorkers + 1); got != thumbnailMaxWorkers {
		t.Fatalf("high worker count = %d, want %d", got, thumbnailMaxWorkers)
	}
}

func TestThumbnailRateLimitedDetection(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "ffmpeg 429",
			text: "[in#0] Error opening input: Server returned 429 Too Many Requests",
			want: true,
		},
		{
			name: "rate limit text",
			text: "provider rate limit exceeded",
			want: true,
		},
		{
			name: "plain 404",
			text: "Error opening input: Server returned 404 Not Found",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := thumbnailRateLimited([]byte(tc.text)); got != tc.want {
				t.Fatalf("thumbnailRateLimited() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestThumbnailRateLimitCooldownBackoff(t *testing.T) {
	cooldown := &thumbnailRateLimitCooldown{}
	if got := cooldown.recordRateLimit(); got != thumbnailRateLimitInitial {
		t.Fatalf("first cooldown = %s, want %s", got, thumbnailRateLimitInitial)
	}
	if got := cooldown.recordRateLimit(); got != thumbnailRateLimitInitial*2 {
		t.Fatalf("second cooldown = %s, want %s", got, thumbnailRateLimitInitial*2)
	}
	for i := 0; i < 10; i++ {
		_ = cooldown.recordRateLimit()
	}
	if cooldown.nextDelay != thumbnailRateLimitMax {
		t.Fatalf("capped cooldown = %s, want %s", cooldown.nextDelay, thumbnailRateLimitMax)
	}
	if time.Until(cooldown.until) <= 0 {
		t.Fatal("expected cooldown deadline to be in the future")
	}
	cooldown.recordSuccess()
	if cooldown.nextDelay != 0 || !cooldown.until.IsZero() {
		t.Fatalf("success should clear cooldown, delay=%s until=%v", cooldown.nextDelay, cooldown.until)
	}
}

func TestStartThumbnailsDisabledBySettings(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Playback.Thumbnails.Enabled = false
	handler := &VideoHandler{
		thumbnailManager: NewThumbnailManager(t.TempDir(), "ffmpeg"),
		configManager:    staticVideoConfigProvider{settings: settings},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/video/thumbnails/start?path=%2Fwebdav%2Fmovie.mkv&duration=3600", nil)
	rr := httptest.NewRecorder()
	handler.StartThumbnails(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusAccepted)
	}
	var startResp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if startResp["status"] != "disabled" {
		t.Fatalf("start status = %v, want disabled", startResp["status"])
	}
	if startResp["started"] != false {
		t.Fatalf("started = %v, want false", startResp["started"])
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/video/thumbnails/status?path=%2Fwebdav%2Fmovie.mkv", nil)
	statusRR := httptest.NewRecorder()
	handler.GetThumbnailsStatus(statusRR, statusReq)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", statusRR.Code, http.StatusOK)
	}
	var statusResp thumbnailStatusResponse
	if err := json.NewDecoder(statusRR.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if statusResp.Status != "disabled" {
		t.Fatalf("thumbnail status = %q, want disabled", statusResp.Status)
	}
}

func TestThumbnailNeedsToneMap(t *testing.T) {
	if thumbnailNeedsToneMap(nil) {
		t.Fatal("nil metadata should not require tone mapping")
	}
	if thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{ColorTransfer: "bt709"}}}) {
		t.Fatal("bt709 stream should not require tone mapping")
	}
	if !thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{ColorTransfer: "smpte2084"}}}) {
		t.Fatal("PQ HDR stream should require tone mapping")
	}
	if !thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{HasDolbyVision: true}}}) {
		t.Fatal("Dolby Vision stream should require tone mapping")
	}
	if !thumbnailNeedsToneMap(&videoMetadataResponse{VideoStreams: []videoStreamSummary{{HdrFormat: "HDR10"}}}) {
		t.Fatal("HDR format should require tone mapping")
	}
}

func TestDolbyVisionProfile5Detection(t *testing.T) {
	cases := []string{"dvhe.05.06", "dvh1.05.09", "profile 5", "5"}
	for _, tc := range cases {
		if !isDolbyVisionProfile5(tc) {
			t.Fatalf("expected %q to be detected as DV profile 5", tc)
		}
	}
	if isDolbyVisionProfile5("dvhe.08.06") {
		t.Fatal("DV profile 8 should not be detected as profile 5")
	}
}

func TestThumbnailToneMapModeSelection(t *testing.T) {
	tests := []struct {
		name      string
		caps      map[string]bool
		toneMap   bool
		dvProfile string
		want      thumbnailToneMapMode
	}{
		{
			name:    "sdr disables tone map",
			caps:    map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true},
			toneMap: false,
			want:    thumbnailToneMapNone,
		},
		{
			name:      "dv5 prefers libplacebo",
			caps:      map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true, "zscale": true, "tonemap": true},
			toneMap:   true,
			dvProfile: "dvhe.05.06",
			want:      thumbnailToneMapLibplacebo,
		},
		{
			name:      "dv5 without usable libplacebo is unsupported",
			caps:      map[string]bool{"zscale": true, "tonemap": true},
			toneMap:   true,
			dvProfile: "dvhe.05.06",
			want:      thumbnailToneMapUnsupported,
		},
		{
			name:      "dv5 with compiled libplacebo but failed runtime is unsupported",
			caps:      map[string]bool{"libplacebo": true, "zscale": true, "tonemap": true},
			toneMap:   true,
			dvProfile: "dvhe.05.06",
			want:      thumbnailToneMapUnsupported,
		},
		{
			name:    "hdr prefers zscale over libplacebo",
			caps:    map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true, "zscale": true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapZscale,
		},
		{
			name:    "hdr uses libplacebo when zscale unavailable",
			caps:    map[string]bool{"libplacebo": true, thumbnailLibplaceboRuntime: true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapLibplacebo,
		},
		{
			name:    "hdr skips unusable libplacebo",
			caps:    map[string]bool{"libplacebo": true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapFFmpeg,
		},
		{
			name:    "hdr falls back to zscale",
			caps:    map[string]bool{"zscale": true, "tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapZscale,
		},
		{
			name:    "hdr falls back to native tonemap",
			caps:    map[string]bool{"tonemap": true},
			toneMap: true,
			want:    thumbnailToneMapFFmpeg,
		},
		{
			name:    "hdr without filters unsupported",
			caps:    map[string]bool{},
			toneMap: true,
			want:    thumbnailToneMapUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewThumbnailManager(t.TempDir(), "ffmpeg")
			manager.filterCaps = tt.caps
			manager.filterOnce.Do(func() {})
			if got := manager.thumbnailToneMapMode(tt.toneMap, tt.dvProfile); got != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestThumbnailFilterUsesDisplayResolution(t *testing.T) {
	filter := thumbnailFilter(thumbnailToneMapNone)
	if !strings.Contains(filter, "scale=640:-2") {
		t.Fatalf("thumbnail filter = %q, want 640px output width", filter)
	}
}

func TestThumbnailFiltersUseExpectedPipelines(t *testing.T) {
	if got := thumbnailFilter(thumbnailToneMapLibplacebo); !strings.Contains(got, "libplacebo=") || !strings.Contains(got, "tonemapping=bt.2390") {
		t.Fatalf("expected libplacebo tone map filter, got %q", got)
	}
	if got := thumbnailFilter(thumbnailToneMapZscale); !strings.Contains(got, "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,zscale=t=linear") || !strings.Contains(got, "tonemap=tonemap=mobius") {
		t.Fatalf("expected zscale tone map filter, got %q", got)
	}
	if got := thumbnailFilter(thumbnailToneMapNone); strings.Contains(got, "tonemap") {
		t.Fatalf("SDR filter should not tone map, got %q", got)
	}
}

func TestManifestFilesCompleteRequiresUsableFiles(t *testing.T) {
	manager := NewThumbnailManager(t.TempDir(), "ffmpeg")
	manifest := &thumbnailManifest{
		Key:       "0123456789abcdef01234567",
		Generated: 2,
		Thumbnails: []thumbnailDetails{
			{TimeSec: 30, File: "thumb-0001.jpg"},
			{TimeSec: 90, File: "thumb-0002.jpg"},
		},
	}
	dir := filepath.Join(manager.baseDir, manifest.Key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir thumbnails: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thumb-0001.jpg"), bytes.Repeat([]byte{1}, thumbnailMinJPEGBytes), 0o644); err != nil {
		t.Fatalf("write usable thumb: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thumb-0002.jpg"), []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatalf("write tiny thumb: %v", err)
	}
	if manager.manifestFilesComplete(manifest) {
		t.Fatal("manifest with tiny thumbnail should be incomplete")
	}
	if err := os.WriteFile(filepath.Join(dir, "thumb-0002.jpg"), bytes.Repeat([]byte{2}, thumbnailMinJPEGBytes), 0o644); err != nil {
		t.Fatalf("write second usable thumb: %v", err)
	}
	if !manager.manifestFilesComplete(manifest) {
		t.Fatal("manifest with all usable thumbnails should be complete")
	}
}

func TestCleanVideoPathParam(t *testing.T) {
	if got := cleanVideoPathParam("/webdav/media/movie.mkv"); got != "/media/movie.mkv" {
		t.Fatalf("unexpected cleaned path: %q", got)
	}
	if got := cleanVideoPathParam("webdav/media/movie.mkv"); got != "/media/movie.mkv" {
		t.Fatalf("unexpected cleaned relative path: %q", got)
	}
}

func TestBuildLocalVideoStreamURL(t *testing.T) {
	h := &VideoHandler{}
	h.SetLocalBaseURL("http://127.0.0.1:7777")

	got := h.buildLocalVideoStreamURL("/debrid/torbox/1/file/2/Movie Name.mkv")
	if !strings.HasPrefix(got, "http://127.0.0.1:7777/api/video/internal-stream?") {
		t.Fatalf("unexpected local stream URL: %q", got)
	}
	if !strings.Contains(got, "path=%2Fdebrid%2Ftorbox%2F1%2Ffile%2F2%2FMovie+Name.mkv") {
		t.Fatalf("expected encoded path in local stream URL, got %q", got)
	}
	if !strings.Contains(got, "transmux=0") {
		t.Fatalf("expected transmux=0 in local stream URL, got %q", got)
	}
}
