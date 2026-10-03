package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"novastream/services/sports"
	"path/filepath"
	"strings"
	"testing"
)

func TestSportsDisabledSettingsAndBackgroundRefresh(t *testing.T) {
	for _, payload := range []string{
		`{"enabled":false,"enabledLeagues":["nba"]}`,
		`{"enabled":true,"enabledLeagues":[]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			service := sports.NewService(t.TempDir())
			h := NewSportsHandler(service, nil, nil)
			h.SetConfigManager(manager)
			w := httptest.NewRecorder()
			h.PutSettings(w, httptest.NewRequest(http.MethodPut, "/sports/settings", strings.NewReader(payload)))
			if w.Code != http.StatusOK {
				t.Fatalf("save: %d %s", w.Code, w.Body.String())
			}
			saved, err := manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(saved.Sports.PollingLeagueIDs()) != 0 {
				t.Fatal("disabled selection did not persist")
			}
			// A nil links handler deliberately fails if the background worker reaches
			// team syncing, including when cached results exist.
			h.RefreshBackground(context.Background(), nil)
			if service.GetStatus().Enabled {
				t.Fatal("disabled service reports enabled")
			}
			// Older clients omit the new switch; they must not re-enable the server.
			w = httptest.NewRecorder()
			h.PutSettings(w, httptest.NewRequest(http.MethodPut, "/sports/settings", strings.NewReader(`{"enabledLeagues":[]}`)))
			if w.Code != http.StatusOK {
				t.Fatalf("legacy save: %d", w.Code)
			}
			again, err := manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			if *again.Sports.Enabled != *saved.Sports.Enabled {
				t.Fatal("legacy client changed enabled switch")
			}
		})
	}
}
