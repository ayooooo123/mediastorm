package openapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpecLoadsAndValidates(t *testing.T) {
	doc, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !doc.IsOpenAPI31OrLater() {
		t.Fatalf("openapi version = %q, want 3.1.x", doc.OpenAPI)
	}
	if _, err := NewValidator(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestSpecCoversClientRoutes pins the route set native clients depend on so a
// removal from the spec is deliberate.
func TestSpecCoversClientRoutes(t *testing.T) {
	doc, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"/api/auth/login":                                {"POST"},
		"/api/users":                                     {"GET"},
		"/api/users/{userId}/pin/verify":                 {"POST"},
		"/api/users/{userId}/history/continue":           {"GET"},
		"/api/users/{userId}/history/series/{seriesId}":  {"GET"},
		"/api/users/{userId}/history/progress":           {"GET", "POST"},
		"/api/users/{userId}/history/watched":            {"GET", "POST"},
		"/api/users/{userId}/details-bundle":             {"GET"},
		"/api/metadata/movies/details":                   {"GET"},
		"/api/metadata/series/details":                   {"GET"},
		"/api/search":                                    {"GET"},
		"/api/users/{userId}/watchlist":                  {"GET", "POST"},
		"/api/users/{userId}/watchlist/{mediaType}/{id}": {"DELETE"},
		"/api/users/{userId}/display-list":               {"GET"},
		"/api/indexers/search":                           {"GET"},
		"/api/indexers/source-health":                    {"GET"},
		"/api/playback/prequeue":                         {"POST"},
		"/api/playback/prequeue/{prequeueId}":            {"GET"},
		"/api/playback/resolve":                          {"POST"},
		"/api/playback/queue/{queueId}":                  {"GET"},
		"/api/video/stream":                              {"GET", "HEAD"},
		"/api/video/segments":                            {"POST"},
		"/api/clients/register":                          {"POST"},
	}
	for path, methods := range want {
		item := doc.Paths.Value(path)
		if item == nil {
			t.Errorf("spec is missing path %s", path)
			continue
		}
		for _, method := range methods {
			if item.GetOperation(method) == nil {
				t.Errorf("spec is missing %s %s", method, path)
			}
		}
	}
	for _, path := range doc.Paths.InMatchingOrder() {
		if strings.Contains(path, "/migrate") {
			t.Errorf("stream migration is deferred and must not be in the spec: %s", path)
		}
	}
}

func TestValidatorFlagsUndocumentedResponseFields(t *testing.T) {
	v, err := NewValidator(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/users/u1/pin/verify", nil)
	header := http.Header{"Content-Type": []string{"application/json"}}

	if err := v.ValidateResponse(req, http.StatusOK, header, []byte(`{"valid":true}`)); err != nil {
		t.Fatalf("documented body rejected: %v", err)
	}
	err = v.ValidateResponse(req, http.StatusOK, header, []byte(`{"valid":true,"surprise":1}`))
	if err == nil || !strings.Contains(err.Error(), "$.surprise") {
		t.Fatalf("undocumented field not flagged: %v", err)
	}
	// Map-typed objects (additionalProperties schemas) accept any key.
	req = httptest.NewRequest(http.MethodGet, "/api/users/u1/history/progress", nil)
	body := `[{"id":"movie:tmdb:1","mediaType":"movie","itemId":"tmdb:1","position":1,"duration":2,"percentWatched":50,"updatedAt":"2026-01-01T00:00:00Z","externalIds":{"anything":"x"}}]`
	if err := v.ValidateResponse(req, http.StatusOK, header, []byte(body)); err != nil {
		t.Fatalf("map keys rejected: %v", err)
	}
	if err := v.ValidateResponse(req, http.StatusTeapot, header, []byte(`{}`)); err == nil {
		t.Fatal("undocumented status accepted")
	}
}
