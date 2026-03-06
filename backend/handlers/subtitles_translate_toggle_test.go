package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
)

func TestSubtitlesHandler_Translate_DisabledBySettings(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Subtitles.EnableTranslatedSubs = false
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := NewSubtitlesHandlerWithConfig(mgr)
	req := httptest.NewRequest(http.MethodGet, "/subtitles/translate?sourceUrl=/subtitles.vtt&targetLanguage=spa", nil)
	rec := httptest.NewRecorder()

	handler.Translate(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("expected disabled error body, got: %s", rec.Body.String())
	}
}
