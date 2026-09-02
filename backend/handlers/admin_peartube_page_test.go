package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
	"novastream/services/peartube"
)

// An install with no relay configured is the shipped default, and PearTube has
// to be selectable and usable in exactly that state — otherwise there is no way
// to configure a relay from the admin UI at all. It lives with the other search
// sources, so what the page must carry is the scraper type and its fields.
func TestSettingsPageOffersPearTubeAsAScraperWithoutARelay(t *testing.T) {
	h, sessionsSvc, _ := newAdminOnboardingTestHandler(t, func(settings *config.Settings) {
		settings.UI.OnboardingCompleted = true
	})
	req := newAdminRequestWithSession(t, sessionsSvc, http.MethodGet, "/admin/settings", true)
	rr := httptest.NewRecorder()

	h.RequireAuth(h.SettingsPage).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !containsAll(body,
		`"torrentScrapers"`,
		`"peartube"`,
		`Contribute watched media`,
		`Enable archive retention`,
		`Watch-only`,
		`PEARTUBE_RELAY_URL`,
	) {
		t.Fatal("settings page does not expose explicit PearTube policy")
	}
	if strings.Contains(body, `PEARTUBE_AUTOSEED`) {
		t.Fatal("settings page still advertises environment-based contribution consent")
	}
	if !strings.Contains(body, `fieldKey.startsWith('config.') && typeof value === 'number'`) {
		t.Fatal("nested config number controls do not serialize decimal strings")
	}
	// The relay's own remedy string is what an operator needs to fix a gated
	// relay without reading logs, so the page must surface it.
	if !strings.Contains(body, `Fix on the relay:`) {
		t.Fatal("the page cannot surface the relay's remedy")
	}
}

func TestTestScraperPearTube(t *testing.T) {
	t.Setenv(peartube.CompanionClientEnv, "mediastorm")
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/status" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-PearTube-Client") == "" || r.Header.Get("X-PearTube-MAC") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"apiVersion": 2,
			"status": "ready",
			"diagnostics": {
				"ready": true,
				"searchAvailable": true,
				"acquisitionAvailable": true,
				"activeAcquisitions": 0,
				"queuedAcquisitions": 0
			}
		}`))
	}))
	defer server.Close()

	h, _, _ := newAdminOnboardingTestHandler(t, nil)
	payload := `{"name":"PearTube","type":"peartube","url":"` + server.URL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/scraper", strings.NewReader(payload))
	rr := httptest.NewRecorder()

	h.TestScraper(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("success = false, error: %s", resp.Error)
	}
	if !strings.Contains(resp.Message, "PearTube relay is working") {
		t.Fatalf("unexpected message: %q", resp.Message)
	}
}

func TestTestScraperPearTubeUnauthorized(t *testing.T) {
	t.Setenv(peartube.CompanionClientEnv, "mediastorm")
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"code":"AUTH_MAC_INVALID","message":"Invalid MAC"}}`))
	}))
	defer server.Close()

	h, _, _ := newAdminOnboardingTestHandler(t, nil)
	payload := `{"name":"PearTube","type":"peartube","url":"` + server.URL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/scraper", strings.NewReader(payload))
	rr := httptest.NewRecorder()

	h.TestScraper(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected success = false, got message: %s", resp.Message)
	}
	if !strings.Contains(resp.Error, "PearTube relay error") {
		t.Fatalf("unexpected error message: %q", resp.Error)
	}
}

func TestTestScraperPearTubeBlankURLIgnoresUnixScheme(t *testing.T) {
	t.Setenv(peartube.CompanionClientEnv, "mediastorm")
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	t.Setenv(peartube.RelayURLEnv, "unix:///tmp/relay.sock")

	h, _, _ := newAdminOnboardingTestHandler(t, nil)
	payload := `{"name":"PearTube","type":"peartube","url":""}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/scraper", strings.NewReader(payload))
	rr := httptest.NewRecorder()

	h.TestScraper(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.Contains(resp.Error, "relay URL must use http or https") {
		t.Fatalf("failed to ignore unix scheme fallback: %s", resp.Error)
	}
}

func TestTestScraperPearTubeExplicitInvalidURL(t *testing.T) {
	t.Setenv(peartube.CompanionClientEnv, "mediastorm")
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))

	h, _, _ := newAdminOnboardingTestHandler(t, nil)
	payload := `{"name":"PearTube","type":"peartube","url":"unix:///tmp/relay.sock"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/scraper", strings.NewReader(payload))
	rr := httptest.NewRecorder()

	h.TestScraper(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected success = false for explicit unix URL, got message: %s", resp.Message)
	}
	if !strings.Contains(resp.Error, "relay URL must use http or https") {
		t.Fatalf("expected error containing 'relay URL must use http or https', got: %q", resp.Error)
	}
}
