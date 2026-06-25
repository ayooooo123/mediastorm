package api

import (
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

func TestIsStreamScopedRequestAllowedLiveSource(t *testing.T) {
	session := models.Session{Scope: models.SessionScopeStream, ScopeResource: "https://example.test/live.m3u8"}
	req := httptest.NewRequest("GET", "/api/live/hls/start?url=https%3A%2F%2Fexample.test%2Flive.m3u8", nil)
	if !isStreamScopedRequestAllowed(req, session) {
		t.Fatal("expected matching live source to be allowed")
	}
}
