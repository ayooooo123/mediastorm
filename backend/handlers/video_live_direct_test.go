package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"

	"github.com/gorilla/mux"
)

func TestServeLiveDirectRelaysBytesAndProviderHeaders(t *testing.T) {
	const streamBytes = "transport-stream-payload"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer portal-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Cookie"); got != "mac=00%3A1A%3A79%3A00%3A00%3A01" {
			t.Errorf("Cookie = %q", got)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, streamBytes)
	}))
	defer upstream.Close()

	handler := NewVideoHandler(false, "", "")
	ticket := handler.registerLiveDirectTarget(liveDirectTarget{
		URL:       upstream.URL + "/play/live.php?stream=12&token=secret",
		AccountID: "account-1",
		Provider:  "stalker",
		BucketKey: "stalker:portal",
		RequestHeaders: map[string]string{
			"Authorization": "Bearer portal-token",
			"Cookie":        "mac=00%3A1A%3A79%3A00%3A00%3A01",
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/video/live-direct/"+ticket+"/stream.ts?profileId=profile-1", nil)
	req = mux.SetURLVars(req, map[string]string{"ticket": ticket})
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "account-1"))
	rec := httptest.NewRecorder()

	handler.ServeLiveDirect(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp2t" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Body.String(); got != streamBytes {
		t.Errorf("body = %q", got)
	}
}

func TestServeLiveDirectRejectsAnotherAccount(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	ticket := handler.registerLiveDirectTarget(liveDirectTarget{
		URL:       "https://provider.example/live.ts?token=secret",
		AccountID: "account-1",
		Provider:  "stalker",
	})
	req := httptest.NewRequest(http.MethodGet, "/video/live-direct/"+ticket+"/stream.ts", nil)
	req = mux.SetURLVars(req, map[string]string{"ticket": ticket})
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "account-2"))
	rec := httptest.NewRecorder()

	handler.ServeLiveDirect(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestLiveDirectTicketExpiryAndUsage(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	ticket := handler.registerLiveDirectTarget(liveDirectTarget{
		URL:       "https://provider.example/live.ts",
		Provider:  "stalker",
		BucketKey: "stalker:portal",
	})

	acquired, ok := handler.acquireLiveDirectTarget(ticket, "")
	if !ok || acquired.URL == "" {
		t.Fatal("ticket was not acquired")
	}
	if got := handler.countActiveLiveDirectUsage(liveStreamTarget{Provider: "stalker", BucketKey: "stalker:portal"}); got != 1 {
		t.Fatalf("active usage = %d, want 1", got)
	}
	handler.releaseLiveDirectTarget(ticket)

	handler.liveDirectMu.Lock()
	handler.liveDirectTargets[ticket].ExpiresAt = time.Now().Add(-time.Second)
	handler.liveDirectMu.Unlock()
	if _, ok := handler.acquireLiveDirectTarget(ticket, ""); ok {
		t.Fatal("expired ticket was accepted")
	}
}

func TestStalkerDirectResponseDoesNotExposePortalSecrets(t *testing.T) {
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "handshake":
			_, _ = io.WriteString(w, `{"js":{"token":"portal-token"}}`)
		case "get_profile":
			_, _ = io.WriteString(w, `{"js":{"id":"1"}}`)
		case "get_genres":
			_, _ = io.WriteString(w, `{"js":[]}`)
		case "get_all_channels":
			_, _ = io.WriteString(w, `{"js":{"data":[{"id":"12","name":"News","cmd":"ffmpeg `+portalURLForRequest(r)+`/play/live.php?stream=12&token=signed-secret"}]}}`)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer portal.Close()

	handler := NewVideoHandlerWithProvider(true, "/bin/echo", "/bin/echo", t.TempDir(), nil)
	handler.SetConfigManager(fakeLiveUsageConfigProvider{settings: stalkerDirectTestSettings(portal.URL)})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{settings: map[string]*models.UserSettings{}})
	req := httptest.NewRequest(http.MethodGet, "/live/hls/start?url="+url.QueryEscape(portal.URL+"/stalker_portal/c/")+"&channelId=12&target=native", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "account-1"))
	rec := httptest.NewRecorder()

	handler.StartLiveHLSSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{portal.URL, "signed-secret", "portal-token", "00:1A:79"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response exposed %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"isDirect":true`) || !strings.Contains(body, "/video/live-direct/") {
		t.Fatalf("unexpected response: %s", body)
	}
}

func portalURLForRequest(r *http.Request) string {
	return "http://" + r.Host
}

func stalkerDirectTestSettings(portalURL string) config.Settings {
	return config.Settings{Live: config.LiveSettings{
		Mode:             "stalker",
		StalkerPortalURL: portalURL + "/stalker_portal/c/",
		StalkerMAC:       "00:1A:79:00:00:01",
		StreamFormat:     "direct",
	}}
}
