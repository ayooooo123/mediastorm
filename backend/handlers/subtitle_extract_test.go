package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSubtitleBuildWebDAVURLSkipsRemoteMedia(t *testing.T) {
	manager := NewSubtitleExtractManager(t.TempDir(), "", "", nil)
	defer manager.Shutdown()
	manager.ConfigureLocalWebDAVAccess("http://localhost:7777", "/webdav", "", "")

	for _, path := range []string{"plexmedia:item-123", "jellyfinmedia:item-456"} {
		t.Run(path, func(t *testing.T) {
			if got := manager.buildWebDAVURL(path); got != "" {
				t.Fatalf("buildWebDAVURL(%q) = %q, want empty", path, got)
			}
		})
	}
}

func TestServeSubtitles_StripsLingeringHEscapesForASS(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "subtitles.ass")
	content := "[Script Info]\n[V4+ Styles]\n[Events]\nDialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,HELLO\\hWORLD\n"
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write subtitles: %v", err)
	}

	manager := NewSubtitleExtractManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	session := &SubtitleExtractSession{
		ID:             "12345678-test-session",
		OutputFormat:   "ass",
		OutputPath:     outputPath,
		CreatedAt:      time.Now(),
		LastAccess:     time.Now(),
		extractionDone: true,
	}

	req := httptest.NewRequest(http.MethodGet, "/video/subtitles/test/subtitles.ass", nil)
	rr := httptest.NewRecorder()

	manager.ServeSubtitles(rr, req, session)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	expected := "[Script Info]\n[V4+ Styles]\n[Events]\nDialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,HELLO WORLD\n"
	if rr.Body.String() != expected {
		t.Fatalf("unexpected body: got %q want %q", rr.Body.String(), expected)
	}
}
