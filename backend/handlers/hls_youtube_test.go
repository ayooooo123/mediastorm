package handlers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestYouTubeTranscodingArgsProduceSegmentSafeHLS(t *testing.T) {
	args := youtubeTranscodingArgs(
		"https://video.example/input.mp4",
		"https://audio.example/input.m4a",
		"",
		"/tmp/output/stream.m3u8",
		"/tmp/output/segment%d.ts",
		0,
	)

	if slices.Contains(args, "copy") {
		t.Fatalf("YouTube HLS must not stream-copy arbitrary video GOPs: %v", args)
	}
	for _, expected := range []string{
		"libx264",
		"yuv420p",
		"expr:gte(t,n_forced*2.000)",
		"independent_segments+temp_file",
	} {
		if !slices.Contains(args, expected) {
			t.Fatalf("expected segment-safe argument %q in %v", expected, args)
		}
	}
}

func TestYouTubeStartupMediaReadyRequiresTwoNonEmptySegments(t *testing.T) {
	outputDir := t.TempDir()
	session := &HLSSession{OutputDir: outputDir}
	playlist := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:6",
		"#EXTINF:2.0,",
		"segment0.ts",
		"#EXTINF:2.0,",
		"segment1.ts",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(outputDir, "stream.m3u8"), []byte(playlist), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "segment0.ts"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, _, ready := youtubeStartupMediaReady(session)
	if ready {
		t.Fatal("session must not be ready before the second segment exists")
	}

	if err := os.WriteFile(filepath.Join(outputDir, "segment1.ts"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, segment0Bytes, segment1Bytes, ready := youtubeStartupMediaReady(session)
	if !ready {
		t.Fatal("session should be ready after two non-empty referenced segments exist")
	}
	if segment0Bytes == 0 || segment1Bytes == 0 {
		t.Fatalf("expected non-empty segments, got segment0=%d segment1=%d", segment0Bytes, segment1Bytes)
	}
}
