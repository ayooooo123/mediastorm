package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/services/peartube"
)

func putPearTubeSettings(t *testing.T, handler *SettingsHandler, stored config.PearTubeSettings) config.Settings {
	t.Helper()

	payload := config.DefaultSettings()
	payload.PearTube = stored
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}

	saved, err := handler.Manager.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	return saved
}

// Every field the admin section exposes has to survive a save/load round trip,
// including a false switch — which is the case a plain bool would have lost.
func TestPutSettingsPersistsEveryPearTubeField(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(mgr)

	saved := putPearTubeSettings(t, handler, config.PearTubeSettings{
		RelayURL: "http://relay.internal:8178",
		Enabled:  new(true),
		AutoSeed: new(false),
	})
	if saved.PearTube.RelayURL != "http://relay.internal:8178" {
		t.Fatalf("relay URL not persisted: %+v", saved.PearTube)
	}
	if saved.PearTube.Enabled == nil || !*saved.PearTube.Enabled {
		t.Fatalf("enabled not persisted: %+v", saved.PearTube)
	}
	if saved.PearTube.AutoSeed == nil || *saved.PearTube.AutoSeed {
		t.Fatalf("a stored autoseed=false was not persisted: %+v", saved.PearTube)
	}

	// And an untouched section stays absent, which is what leaves the
	// environment in charge instead of silently pinning the switches to off.
	cleared := putPearTubeSettings(t, handler, config.PearTubeSettings{})
	if cleared.PearTube.RelayURL != "" || cleared.PearTube.Enabled != nil || cleared.PearTube.AutoSeed != nil {
		t.Fatalf("an unset section persisted values: %+v", cleared.PearTube)
	}
}

// A save has to repoint the running integration, not wait for a restart.
func TestPutSettingsAppliesPearTubeWithoutARestart(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(mgr)
	pearTube := NewPearTubeHandler(nil)
	handler.SetPearTubeConfigurer(pearTube)
	t.Cleanup(func() { pearTube.ApplyPearTubeSettings(config.PearTubeSettings{}) })

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entities":[],"total":0}`))
	}))
	defer relay.Close()

	putPearTubeSettings(t, handler, config.PearTubeSettings{RelayURL: relay.URL})

	body := pearTubeStatusBody(t, pearTube)
	if body.State != "ready" {
		t.Fatalf("state = %q, want ready: %+v", body.State, body)
	}
	if body.RelayURL != relay.URL {
		t.Fatalf("relay URL = %q, want %q", body.RelayURL, relay.URL)
	}
	// The seed endpoint answered "not configured" before the save.
	rec := httptest.NewRecorder()
	pearTube.Seed(rec, httptest.NewRequest(http.MethodPost, "/api/p2p/seed", bytes.NewReader([]byte(`{}`))))
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("seeding is still unavailable after a relay was saved")
	}

	// Clearing the URL has to switch the whole integration back off.
	putPearTubeSettings(t, handler, config.PearTubeSettings{})
	if body := pearTubeStatusBody(t, pearTube); body.State != "disabled" || body.Enabled {
		t.Fatalf("clearing the relay URL left the integration on: %+v", body)
	}
}

// The precedence rule the settings page documents, exercised end to end: what is
// stored wins, and the environment is only the default for an absent field.
func TestPearTubePrecedenceBetweenStoredValuesAndTheEnvironment(t *testing.T) {
	t.Setenv(peartube.RelayURLEnv, "http://relay.from-env:8178")
	t.Setenv(peartube.AutoSeedEnv, "0")

	pearTube := NewPearTubeHandler(nil)
	t.Cleanup(func() { pearTube.ApplyPearTubeSettings(config.PearTubeSettings{}) })

	// Nothing stored: the environment supplies both, and says so.
	pearTube.ApplyPearTubeSettings(config.PearTubeSettings{})
	body := pearTubeStatusBody(t, pearTube)
	if body.RelayURL != "http://relay.from-env:8178" || !body.FromEnv["relayUrl"] {
		t.Fatalf("relay URL did not come from the environment: %+v", body)
	}
	if body.AutoSeed || !body.FromEnv["autoSeed"] {
		t.Fatalf("autoseed did not come from the environment: %+v", body)
	}

	// Stored values win, field by field, and are no longer attributed to the
	// environment.
	pearTube.ApplyPearTubeSettings(config.PearTubeSettings{
		RelayURL: "http://relay.from-settings:9000",
		AutoSeed: new(true),
	})
	body = pearTubeStatusBody(t, pearTube)
	if body.RelayURL != "http://relay.from-settings:9000" || body.FromEnv["relayUrl"] {
		t.Fatalf("stored relay URL did not win: %+v", body)
	}
	if !body.AutoSeed || body.FromEnv["autoSeed"] {
		t.Fatalf("stored autoseed did not win: %+v", body)
	}

	// A stored disable beats an environment that named a relay.
	pearTube.ApplyPearTubeSettings(config.PearTubeSettings{Enabled: new(false)})
	if body := pearTubeStatusBody(t, pearTube); body.Enabled || body.State != "disabled" {
		t.Fatalf("stored disable was overridden by the environment: %+v", body)
	}
}

// With no relay anywhere the section still has to render and report itself as
// disabled rather than erroring or looking broken.
func TestPearTubeSectionRendersAndReportsDisabledWithoutARelay(t *testing.T) {
	section, ok := SettingsSchema["peartube"].(map[string]interface{})
	if !ok {
		t.Fatal("peartube settings schema is missing")
	}
	if section["group"] != "services" {
		t.Fatalf("peartube section group = %v, want services", section["group"])
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("peartube section has no fields")
	}
	for field, wantType := range map[string]string{"relayUrl": "text", "enabled": "boolean", "autoSeed": "boolean"} {
		def, ok := fields[field].(map[string]interface{})
		if !ok {
			t.Fatalf("peartube field %q is missing", field)
		}
		if def["type"] != wantType {
			t.Fatalf("peartube field %q type = %v, want %s", field, def["type"], wantType)
		}
	}

	pearTube := NewPearTubeHandler(nil)
	t.Cleanup(func() { pearTube.ApplyPearTubeSettings(config.PearTubeSettings{}) })
	pearTube.ApplyPearTubeSettings(config.PearTubeSettings{})

	body := pearTubeStatusBody(t, pearTube)
	if body.Enabled || body.State != "disabled" {
		t.Fatalf("status = %+v, want a disabled integration", body)
	}
	if body.FromEnv == nil {
		t.Fatal("status omitted the field provenance the settings page renders")
	}
}

type pearTubeStatus struct {
	Enabled  bool            `json:"enabled"`
	State    string          `json:"state"`
	RelayURL string          `json:"relayUrl"`
	AutoSeed bool            `json:"autoSeed"`
	Remedy   string          `json:"remedy"`
	FromEnv  map[string]bool `json:"fromEnv"`
}

func pearTubeStatusBody(t *testing.T, handler *PearTubeHandler) pearTubeStatus {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.Status(rec, httptest.NewRequest(http.MethodGet, "/admin/api/p2p/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body pearTubeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return body
}
