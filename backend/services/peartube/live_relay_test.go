package peartube

// A live PearTube relay, end to end.
//
// Every other test in this package runs against an httptest stub that answers
// the shapes this client expects. That proves the client parses what it was
// written to parse, and nothing about whether a real relay speaks the same
// contract - which is exactly where an integration breaks: a route renamed, a
// field dropped, a default port that no relay listens on.
//
// This test needs a relay holding at least one TMDB-tagged publication, so it
// is opt-in:
//
//	peartube-relay ui --storage /tmp/pt-live/storage --host 127.0.0.1 --port 8174
//	curl -X POST http://127.0.0.1:8174/api/v1/archive \
//	  -F file=@movie.mp4 -F contentKind=movie -F tmdbId=603 -F tmdbTitle='The Matrix'
//	PEARTUBE_LIVE_RELAY=http://127.0.0.1:8174 go test ./backend/services/peartube -run Live

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func liveRelay(t *testing.T) *Client {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("PEARTUBE_LIVE_RELAY"))
	if base == "" {
		t.Skip("set PEARTUBE_LIVE_RELAY to a running relay base URL to run this test")
	}
	client, err := New(base)
	if err != nil {
		t.Fatalf("New(%q): %v", base, err)
	}
	return client
}

func TestLiveRelayServesCatalogAndStream(t *testing.T) {
	client := liveRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	state := client.Probe(ctx)
	if !state.Reachable {
		t.Fatalf("relay %s is not reachable: %s", state.RelayURL, state.Detail)
	}
	if state.NotOpen {
		t.Fatalf("relay refuses to enumerate: %s", state.Remedy)
	}
	if state.CatalogEntities == 0 {
		t.Fatal("relay catalog is empty; seed a publication before running this test")
	}

	entities, err := client.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	// Search by the coordinates the relay itself reported, so the assertion is
	// about the contract rather than about whatever happens to be seeded.
	var (
		wantTitle string
		wantTMDB  string
	)
	for _, entity := range entities {
		for _, source := range entity.Sources {
			if coords, ok := coordinatesForSource(entity, source); ok && coords.Kind == "movie" {
				wantTitle, wantTMDB = entity.Title, coords.TMDBID
				break
			}
		}
		if wantTMDB != "" {
			break
		}
	}
	if wantTMDB == "" {
		t.Skip("no TMDB-tagged movie in the live catalog")
	}

	results, err := client.Search(ctx, SearchRequest{Title: wantTitle, TMDBID: wantTMDB, MediaType: "movie"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("search for tmdb=%s matched nothing in a catalog that reported it", wantTMDB)
	}

	resolution, err := ResolvePlayback(client, results[0])
	if err != nil {
		t.Fatalf("ResolvePlayback: %v", err)
	}
	if resolution.HealthStatus != HealthStatus {
		t.Fatalf("health status = %q, want %q", resolution.HealthStatus, HealthStatus)
	}
	if !strings.HasPrefix(resolution.WebDAVPath, client.BaseURL()+apiPrefix+"/stream/") {
		t.Fatalf("playback path %q does not address the configured relay", resolution.WebDAVPath)
	}

	// The resolution is only worth anything if bytes come back from it, so
	// fetch a range the way a player seeking into a file would.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolution.WebDAVPath, nil)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream fetch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range request status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("accept-ranges = %q, want bytes", got)
	}
	if got := resp.Header.Get("Content-Range"); !strings.HasPrefix(got, "bytes 0-1023/") {
		t.Fatalf("content-range = %q, want bytes 0-1023/<total>", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	if len(body) != 1024 {
		t.Fatalf("range body = %d bytes, want 1024", len(body))
	}
	if resolution.FileSize > 0 {
		total := strings.TrimPrefix(resp.Header.Get("Content-Range"), "bytes 0-1023/")
		if size, convErr := strconv.ParseInt(total, 10, 64); convErr == nil && size != resolution.FileSize {
			t.Fatalf("relay reports %d bytes, catalog reported %d", size, resolution.FileSize)
		}
	}
}
