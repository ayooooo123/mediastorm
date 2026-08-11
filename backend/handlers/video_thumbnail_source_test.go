package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type thumbnailRoundTripFunc func(*http.Request) (*http.Response, error)

func (f thumbnailRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestThumbnailSourceBridgeForwardsRangeAndStoredAuth(t *testing.T) {
	key := strings.Repeat("a", 24)
	bridge := &thumbnailSourceBridge{
		secret: "test-secret",
		sessions: map[string]thumbnailSourceSession{
			key: {sourceURL: "https://media.example/movie.mkv", authHeader: "Authorization: Basic stored\r\n"},
		},
	}
	bridge.client = &http.Client{Transport: thumbnailRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Range"); got != "bytes=100-199" {
			t.Fatalf("upstream Range = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Basic stored" {
			t.Fatalf("upstream Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Range":  []string{"bytes 100-102/1000"},
				"Content-Length": []string{"3"},
			},
			Body: io.NopCloser(strings.NewReader("abc")),
		}, nil
	})}

	req := httptest.NewRequest(http.MethodGet, "/thumbnail-source/test-secret/"+key, nil)
	req.Header.Set("Range", "bytes=100-199")
	req.Header.Set("Authorization", "Bearer client-must-not-pass-through")
	rec := httptest.NewRecorder()
	bridge.serveHTTP(rec, req)

	if rec.Code != http.StatusPartialContent || rec.Body.String() != "abc" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 100-102/1000" {
		t.Fatalf("response Content-Range = %q", got)
	}
}

func TestThumbnailRedirectPolicyPreservesAuthOnlyWithinOrigin(t *testing.T) {
	original := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "webdav.example", Path: "/movie.mkv"},
		Header: http.Header{"Authorization": []string{"Basic stored"}},
	}

	sameOrigin := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "webdav.example", Path: "/redirected/movie.mkv"},
		Header: make(http.Header),
	}
	if err := thumbnailRedirectPolicy(sameOrigin, []*http.Request{original}); err != nil {
		t.Fatalf("same-origin redirect policy: %v", err)
	}
	if got := sameOrigin.Header.Get("Authorization"); got != "Basic stored" {
		t.Fatalf("same-origin Authorization = %q", got)
	}

	crossOrigin := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie.mkv"},
		Header: make(http.Header),
	}
	if err := thumbnailRedirectPolicy(crossOrigin, []*http.Request{original}); err != nil {
		t.Fatalf("cross-origin redirect policy: %v", err)
	}
	if got := crossOrigin.Header.Get("Authorization"); got != "" {
		t.Fatalf("cross-origin Authorization = %q, want empty", got)
	}
}
