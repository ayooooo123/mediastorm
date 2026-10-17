package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"novastream/config"
)

type fakeBridgeController struct {
	applied []config.JellyfinBridgeSettings
}

func (f *fakeBridgeController) Apply(cfg config.JellyfinBridgeSettings) error {
	f.applied = append(f.applied, cfg)
	return nil
}

func (f *fakeBridgeController) Status() JellyfinBridgeStatus {
	if n := len(f.applied); n > 0 && f.applied[n-1].Enabled {
		return JellyfinBridgeStatus{Running: true, Port: f.applied[n-1].Port}
	}
	return JellyfinBridgeStatus{}
}

func TestJellyfinBridgeAdminSaveApplies(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	stored, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored.JellyfinBridge.ServerID = "keepme"
	if err := mgr.Save(stored); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeBridgeController{}
	admin := NewJellyfinBridgeAdmin(mgr, ctrl)

	rec := httptest.NewRecorder()
	admin.Get(rec, httptest.NewRequest(http.MethodGet, "/admin/api/jellyfin-bridge", nil))
	var got jellyfinBridgeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || len(got.Fields) == 0 || got.Status.Running {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}

	if got.WebClient.Found || got.WebClient.Dir != defaultJellyfinWebDir {
		t.Fatalf("web client = %+v, want none in the default dir", got.WebClient)
	}

	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(config.JellyfinBridgeSettings{Enabled: true, Port: 8097, ServerID: "attacker", TrustedProxies: []string{" 10.0.0.1 ", ""}, WebClientDir: " " + web + " "})
	rec = httptest.NewRecorder()
	admin.Put(rec, httptest.NewRequest(http.MethodPut, "/admin/api/jellyfin-bridge", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}
	var put jellyfinBridgeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil || !put.WebClient.Found || put.WebClient.Dir != web {
		t.Fatalf("web client after save = %+v", put.WebClient)
	}
	if len(ctrl.applied) != 1 || !ctrl.applied[0].Enabled || ctrl.applied[0].Port != 8097 {
		t.Fatalf("applied = %+v", ctrl.applied)
	}
	saved, _ := mgr.Load()
	if !saved.JellyfinBridge.Enabled || saved.JellyfinBridge.ServerID != "keepme" || len(saved.JellyfinBridge.TrustedProxies) != 1 || saved.JellyfinBridge.TrustedProxies[0] != "10.0.0.1" {
		t.Fatalf("saved = %+v", saved.JellyfinBridge)
	}

	body, _ = json.Marshal(config.JellyfinBridgeSettings{Enabled: true, Port: 0})
	rec = httptest.NewRecorder()
	admin.Put(rec, httptest.NewRequest(http.MethodPut, "/admin/api/jellyfin-bridge", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest || len(ctrl.applied) != 1 {
		t.Fatalf("invalid port accepted: %d", rec.Code)
	}
}
