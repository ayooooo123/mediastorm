package handlers

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cast live runs the compatibility branch of liveHLSOutputArgs: it re-encodes, but production
// is unpaced, so FFmpeg's production-paced delete_segments removes segments a lagging receiver
// (Chromecast starts ~14s behind the edge via EXT-X-START) has not fetched yet. Cast must run
// the native retention contract instead: wide window, no delete_segments, consumption-paced
// pruning in ServeSegment.
func TestLiveHLSOutputArgsCastUsesConsumptionPacedRetention(t *testing.T) {
	args := liveHLSOutputArgs("cast", "/tmp/live/segment%d.ts", "/tmp/live/stream.m3u8")
	joined := strings.Join(args, " ")

	for _, expected := range []string{
		"-c:v libx264",
		"independent_segments+temp_file",
		fmt.Sprintf("-hls_list_size %d", liveNativeHLSListSize),
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("cast live args %q missing %q", joined, expected)
		}
	}
	if strings.Contains(joined, "delete_segments") {
		t.Fatalf("cast live args must not use delete_segments: %q", joined)
	}
}

// A starved-and-rebuilt Cast session continues the segment timeline rather than starting over,
// and marks the seam so the receiver re-initialises instead of decoding across it.
func TestLiveHLSOutputArgsCastResumeAdvancesAndMarksDiscontinuity(t *testing.T) {
	resumed := strings.Join(liveHLSOutputArgs("cast", "/out/segment%d.ts", "/out/stream.m3u8", 207), " ")
	if strings.Contains(resumed, "append_list") {
		t.Fatalf("a cast restart must not use append_list on rolling live playlists: %s", resumed)
	}
	if !strings.Contains(resumed, "discont_start") {
		t.Fatalf("a cast restart must mark the timeline cut with discont_start: %s", resumed)
	}
	if !strings.Contains(resumed, "-start_number 207") {
		t.Fatalf("a cast restart must continue the segment numbering: %s", resumed)
	}
	if strings.Contains(resumed, "delete_segments") {
		t.Fatalf("a cast restart must still avoid delete_segments: %s", resumed)
	}
}

// Serving a segment prunes what the player has already consumed for every live target, not
// just native: without FFmpeg delete_segments on the compat branch, this keep-behind window
// is the only thing bounding a Cast live session's disk use while the player is watching.
func TestServeSegmentPrunesConsumedSegmentsForCastLive(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= 80; i++ {
		path := filepath.Join(dir, fmt.Sprintf("segment%d.ts", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	session := &HLSSession{
		ID:                  "cast-live-prune",
		OutputDir:           dir,
		IsLive:              true,
		PlaybackTarget:      "cast",
		MaxSegmentRequested: -1,
		LastSegmentServed:   -1,
	}
	m := &HLSManager{sessions: map[string]*HLSSession{session.ID: session}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/hls/"+session.ID+"/segment80.ts", nil)
	m.ServeSegment(recorder, request, session.ID, "segment80.ts")
	if recorder.Code != 200 {
		t.Fatalf("serving segment80.ts got status %d", recorder.Code)
	}

	// Pruning runs in a goroutine off the served high-water mark; wait for it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "segment0.ts")); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// highWater=80, keepBehind=60 → cutoff=20: 0..20 pruned, 21..80 retained.
	for i := 0; i <= 20; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("segment%d.ts", i))); !os.IsNotExist(err) {
			t.Fatalf("segment%d.ts should have been pruned for a cast live session", i)
		}
	}
	for i := 21; i <= 80; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("segment%d.ts", i))); err != nil {
			t.Fatalf("segment%d.ts must survive the keep-behind window: %v", i, err)
		}
	}
}
