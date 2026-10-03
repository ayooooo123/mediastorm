package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"novastream/config"
)

func TestGlobalSettingsSavePreservesSportsUpdateSelection(t *testing.T) {
	for _, tc := range []struct {
		name            string
		existingEnabled bool
		existingLeagues []string
		incomingSports  string
		wantEnabled     bool
		wantLeagues     []string
	}{
		{"legacy sports settings", false, []string{"nba"}, `{"enabledLeagues":["nba"]}`, false, []string{"nba"}},
		{"omitted sports settings", false, []string{"nba"}, "", false, []string{"nba"}},
		{"omitted empty selection", true, []string{}, `{"enabled":true}`, true, []string{}},
		{"omitted sports with empty selection", true, []string{}, "", true, []string{}},
		{"null update settings", false, []string{"nba"}, `{"enabled":null,"enabledLeagues":null}`, false, []string{"nba"}},
		{"explicit enable", false, []string{"nba"}, `{"enabled":true,"enabledLeagues":["nba"]}`, true, []string{"nba"}},
		{"explicit disable", true, []string{"nba"}, `{"enabled":false,"enabledLeagues":["nba"]}`, false, []string{"nba"}},
		{"explicit empty selection", true, []string{"nba"}, `{"enabled":true,"enabledLeagues":[]}`, true, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			settings := config.DefaultSettings()
			settings.Sports.Enabled = &tc.existingEnabled
			settings.Sports.EnabledLeagues = tc.existingLeagues
			if err := manager.Save(settings); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if tc.incomingSports == "" {
				delete(payload, "sports")
			} else {
				payload["sports"] = json.RawMessage(tc.incomingSports)
			}
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			NewSettingsHandler(manager).PutSettings(response, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body)))
			if response.Code != http.StatusOK {
				t.Fatalf("save returned %d: %s", response.Code, response.Body.String())
			}
			saved, err := manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			var returned config.Settings
			if err := json.Unmarshal(response.Body.Bytes(), &returned); err != nil {
				t.Fatal(err)
			}
			for name, sports := range map[string]config.SportsSettings{"saved": saved.Sports, "returned": returned.Sports} {
				if sports.Enabled == nil || *sports.Enabled != tc.wantEnabled {
					t.Errorf("%s enabled = %v, want %v", name, sports.Enabled, tc.wantEnabled)
				}
				if !reflect.DeepEqual(sports.EnabledLeagues, tc.wantLeagues) {
					t.Errorf("%s leagues = %#v, want %#v", name, sports.EnabledLeagues, tc.wantLeagues)
				}
			}
		})
	}
}
