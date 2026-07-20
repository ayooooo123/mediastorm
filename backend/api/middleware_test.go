package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/models"
)

func TestIsStreamScopedRequestAllowed(t *testing.T) {
	session := models.Session{Scope: models.SessionScopeStream, ScopeResource: "/movie.mkv"}
	allowed := []string{
		"/api/video/hls/abc/stream.m3u8",
		"/api/video/metadata?path=%2Fmovie.mkv",
		"/api/video/stream?path=/movie.mkv",
		"/api/video/stream/Movie.mkv?path=webdav/movie.mkv",
		"/api/video/direct-url?path=/movie.mkv",
		"/api/video/subtitles/start?path=/movie.mkv",
		"/api/video/hls/start?path=__shared_source__",
	}
	for _, target := range allowed {
		req := httptest.NewRequest("GET", target, nil)
		if !isStreamScopedRequestAllowed(req, session) {
			t.Errorf("expected %q to be allowed for matching stream-scoped session", target)
		}
	}

	denied := []string{
		"/api/settings",
		"/api/users/u1/history/progress",
		"/api/share/create",
		"/api/watchlist",
		"/api/discover/new",
		"/api/videos",
		"/api/video/metadata?path=/other.mkv",
		"/api/video/stream?path=/other.mkv",
		"/api/live/hls/start?url=https://example.test/live.m3u8",
		"/api/live/recordings/1/stream",
	}
	for _, target := range denied {
		req := httptest.NewRequest("GET", target, nil)
		if isStreamScopedRequestAllowed(req, session) {
			t.Errorf("expected %q to be denied for stream-scoped session", target)
		}
	}
}

func TestLocalhostOnlyMiddlewareUsesTCPPeer(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := localhostOnlyMiddleware(next)

	spoofed := httptest.NewRequest(http.MethodGet, "http://localhost/api/debug/runtime", nil)
	spoofed.RemoteAddr = "198.51.100.10:4321"
	spoofedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(spoofedRecorder, spoofed)
	if spoofedRecorder.Code != http.StatusForbidden {
		t.Fatalf("spoofed Host request status = %d, want 403", spoofedRecorder.Code)
	}

	local := httptest.NewRequest(http.MethodGet, "http://example.test/api/debug/runtime", nil)
	local.RemoteAddr = "127.0.0.1:4321"
	localRecorder := httptest.NewRecorder()
	handler.ServeHTTP(localRecorder, local)
	if localRecorder.Code != http.StatusNoContent {
		t.Fatalf("loopback request status = %d, want 204", localRecorder.Code)
	}
}

func TestIsStreamScopedRequestAllowedLiveSource(t *testing.T) {
	session := models.Session{Scope: models.SessionScopeStream, ScopeResource: "https://example.test/live.m3u8"}
	req := httptest.NewRequest("GET", "/api/live/hls/start?url=https%3A%2F%2Fexample.test%2Flive.m3u8", nil)
	if !isStreamScopedRequestAllowed(req, session) {
		t.Fatal("expected matching live source to be allowed")
	}
}

func TestExtractTokenRestrictsQueryTokensToMediaRoutes(t *testing.T) {
	mediaReq := httptest.NewRequest("GET", "/api/video/stream?token=media-token&path=/movie.mkv", nil)
	if got := extractToken(mediaReq); got != "media-token" {
		t.Fatalf("media query token = %q, want media-token", got)
	}

	artworkReq := httptest.NewRequest("GET", "/api/library/items/item-1/artwork/poster?token=art-token", nil)
	if got := extractToken(artworkReq); got != "art-token" {
		t.Fatalf("artwork query token = %q, want art-token", got)
	}

	settingsReq := httptest.NewRequest("GET", "/api/settings?token=leaked-token", nil)
	if got := extractToken(settingsReq); got != "" {
		t.Fatalf("general API accepted query token %q", got)
	}
}
