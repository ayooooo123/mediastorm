package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadAdminDashboardLayoutUsesDefaultForInvalidStoredData(t *testing.T) {
	valid, err := json.Marshal(defaultAdminDashboardLayout())
	if err != nil {
		t.Fatalf("marshal default layout: %v", err)
	}

	tests := map[string][]byte{
		"malformed JSON":     []byte(`{"version":`),
		"multiple values":    append(append([]byte(nil), valid...), []byte(` {}`)...),
		"unknown field":      []byte(`{"version":1,"modules":[],"unexpected":true}`),
		"oversized document": bytes.Repeat([]byte(" "), adminDashboardMaxBodyBytes+1),
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), adminDashboardLayoutFile)
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatalf("write stored layout: %v", err)
			}

			layout, err := loadAdminDashboardLayout(path)
			if err != nil {
				t.Fatalf("load invalid stored layout: %v", err)
			}
			if !reflect.DeepEqual(layout, defaultAdminDashboardLayout()) {
				t.Fatalf("invalid stored layout did not fall back to defaults: %#v", layout)
			}
		})
	}
}

func TestLoadAdminDashboardLayoutNormalizesStoredModules(t *testing.T) {
	layout := defaultAdminDashboardLayout()
	for i := range layout.Modules {
		layout.Modules[i].Y += 10
	}
	path := filepath.Join(t.TempDir(), adminDashboardLayoutFile)
	if err := saveAdminDashboardLayout(path, layout); err != nil {
		t.Fatalf("save stored layout: %v", err)
	}

	loaded, err := loadAdminDashboardLayout(path)
	if err != nil {
		t.Fatalf("load stored layout: %v", err)
	}
	if !reflect.DeepEqual(loaded, defaultAdminDashboardLayout()) {
		t.Fatalf("stored layout was not compacted to defaults: %#v", loaded)
	}
}

func TestValidateAdminDashboardLayoutRejectsInvalidModules(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		layout := defaultAdminDashboardLayout()
		layout.Modules[1].ID = layout.Modules[0].ID
		if _, err := validateAdminDashboardLayout(layout); err == nil || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("expected duplicate module error, got %v", err)
		}
	})

	t.Run("overlap", func(t *testing.T) {
		layout := defaultAdminDashboardLayout()
		layout.Modules[1].X = layout.Modules[0].X
		layout.Modules[1].Y = layout.Modules[0].Y
		if _, err := validateAdminDashboardLayout(layout); err == nil || !strings.Contains(err.Error(), "must not overlap") {
			t.Fatalf("expected overlap error, got %v", err)
		}
	})

	t.Run("outside grid", func(t *testing.T) {
		layout := defaultAdminDashboardLayout()
		layout.Modules[0].Y = adminDashboardMaxRows
		if _, err := validateAdminDashboardLayout(layout); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("expected grid bounds error, got %v", err)
		}
	})
}

func TestAdminDashboardLayoutHandlersSaveGetAndReset(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	handler := &AdminUIHandler{settingsPath: settingsPath}
	layout := defaultAdminDashboardLayout()
	payload, err := json.Marshal(layout)
	if err != nil {
		t.Fatalf("marshal layout: %v", err)
	}

	saveRecorder := httptest.NewRecorder()
	handler.SaveDashboardLayout(saveRecorder, httptest.NewRequest(http.MethodPut, "/admin/api/dashboard/layout", bytes.NewReader(payload)))
	if saveRecorder.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", saveRecorder.Code, saveRecorder.Body.String())
	}
	layoutPath := filepath.Join(filepath.Dir(settingsPath), adminDashboardLayoutFile)
	if _, err := os.Stat(layoutPath); err != nil {
		t.Fatalf("saved layout not found: %v", err)
	}

	getRecorder := httptest.NewRecorder()
	handler.GetDashboardLayout(getRecorder, httptest.NewRequest(http.MethodGet, "/admin/api/dashboard/layout", nil))
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRecorder.Code, getRecorder.Body.String())
	}
	var response adminDashboardLayoutResponse
	if err := json.NewDecoder(getRecorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if len(response.Modules) != len(adminDashboardModules) || len(response.Definitions) != len(adminDashboardModules) {
		t.Fatalf("incomplete response: %d modules, %d definitions", len(response.Modules), len(response.Definitions))
	}

	resetRecorder := httptest.NewRecorder()
	handler.ResetDashboardLayout(resetRecorder, httptest.NewRequest(http.MethodDelete, "/admin/api/dashboard/layout", nil))
	if resetRecorder.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", resetRecorder.Code, resetRecorder.Body.String())
	}
	if _, err := os.Stat(layoutPath); !os.IsNotExist(err) {
		t.Fatalf("saved layout still exists after reset: %v", err)
	}
}

func TestGetDashboardLayoutRecoversFromMalformedFile(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	layoutPath := filepath.Join(filepath.Dir(settingsPath), adminDashboardLayoutFile)
	if err := os.WriteFile(layoutPath, []byte(`{"version":`), 0o600); err != nil {
		t.Fatalf("write malformed layout: %v", err)
	}
	handler := &AdminUIHandler{settingsPath: settingsPath}

	recorder := httptest.NewRecorder()
	handler.GetDashboardLayout(recorder, httptest.NewRequest(http.MethodGet, "/admin/api/dashboard/layout", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response adminDashboardLayoutResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(response.Modules, defaultAdminDashboardLayout().Modules) {
		t.Fatalf("malformed file did not return default modules: %#v", response.Modules)
	}
}

func TestDashboardLayoutControllerFallbackHonorsModuleAvailability(t *testing.T) {
	source, err := staticAssets.ReadFile("static/admin-dashboard-layout-v1.js")
	if err != nil {
		t.Fatalf("read dashboard layout controller: %v", err)
	}
	for _, marker := range []string{
		"availability: new Map([['vod-stream-limits', false]])",
		"if (state.fallback) {\n            applyFallbackVisibility();",
		"const unavailable = state.availability.get(id) === false;",
		"element.style.display = unavailable || hiddenByDetail ? 'none' : '';",
	} {
		if !bytes.Contains(source, []byte(marker)) {
			t.Fatalf("dashboard fallback is missing availability behavior %q", marker)
		}
	}
}

func TestGridStackVendoredChecksumsMatchLicense(t *testing.T) {
	license, err := staticAssets.ReadFile("static/gridstack-13.2.0.LICENSE.txt")
	if err != nil {
		t.Fatalf("read GridStack license: %v", err)
	}
	for _, name := range []string{"gridstack-13.2.0-all.js", "gridstack-13.2.0.min.css"} {
		contents, err := staticAssets.ReadFile("static/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(contents))
		if !bytes.Contains(license, []byte(checksum)) {
			t.Fatalf("%s checksum %s is not recorded in the GridStack license", name, checksum)
		}
	}
}
