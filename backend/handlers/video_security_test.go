package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
)

type staticSecurityConfigProvider struct {
	settings config.Settings
}

func (p staticSecurityConfigProvider) Load() (config.Settings, error) {
	return p.settings, nil
}

func TestAllowedPrivateMediaOriginPermitsConfiguredEndpointOnly(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.SetConfigManager(staticSecurityConfigProvider{settings: config.Settings{
		Server: config.ServerSettings{
			AllowedPrivateMediaOrigins: []string{"http://localhost:8080"},
		},
	}})

	req := httptest.NewRequest(http.MethodGet, "/video/hls/start", nil)
	allowed := httptest.NewRecorder()
	if !handler.requireAllowedExternalPath(allowed, req, "http://localhost:8080/adapters/addon/title.mkv") {
		t.Fatalf("configured private media origin rejected: status=%d body=%s", allowed.Code, allowed.Body.String())
	}

	rejected := httptest.NewRecorder()
	if handler.requireAllowedExternalPath(rejected, req, "http://localhost:7777/api/settings") {
		t.Fatal("same private host on an unconfigured port was allowed")
	}
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("rejected status = %d, want %d", rejected.Code, http.StatusBadRequest)
	}
}

func TestConfiguredProviderPrivateOriginRemainsAllowed(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.SetConfigManager(staticSecurityConfigProvider{settings: config.Settings{
		TorrentScrapers: []config.TorrentScraperConfig{{
			Name:    "AIOStreams",
			Type:    "aiostreams",
			URL:     "http://localhost:8080/stremio/config/manifest.json",
			Enabled: true,
		}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/video/hls/start", nil)
	recorder := httptest.NewRecorder()
	if !handler.requireAllowedExternalPath(recorder, req, "http://localhost:8080/adapters/addon/title.mkv") {
		t.Fatalf("configured provider private origin rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
