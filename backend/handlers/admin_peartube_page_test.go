package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
)

// An install with no relay configured is the shipped default, and the settings
// section has to be there and be usable in exactly that state — otherwise there
// is no way to configure a relay from the admin UI at all.
func TestSettingsPageRendersPearTubeSectionWithoutARelay(t *testing.T) {
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
		`"peartube"`,
		`PearTube (peer-to-peer)`,
		`"relayUrl"`,
		`Relay URL`,
		`"autoSeed"`,
		`Auto-seed what I watch`,
		`Enable PearTube`,
	) {
		t.Fatal("settings page did not render the PearTube section")
	}
	// The section has to state the precedence rule and the live-apply promise,
	// because neither is discoverable from the fields themselves.
	if !containsAll(body, `PEARTUBE_RELAY_URL`, `PEARTUBE_AUTOSEED`, `no restart`) {
		t.Fatal("PearTube section did not document precedence or live apply")
	}
	// The relay's own remedy string is what an operator needs to fix a gated
	// relay without reading logs, so the page must surface it.
	if !strings.Contains(body, `Fix on the relay:`) {
		t.Fatal("PearTube section cannot surface the relay's remedy")
	}
}
