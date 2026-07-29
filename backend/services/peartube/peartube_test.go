package peartube

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novastream/models"
)

const catalogBody = `{
  "schema": "peartube.catalog",
  "version": 1,
  "entities": [
    {"entityId": "tmdb:movie:603", "entityKind": "movie", "title": "The Matrix", "year": 1999,
     "sources": [{"publicationId": "pub-matrix", "publisherId": "abcdef0123456789", "renditionId": "rend-1", "coreKey": "cafe", "coreLength": 12, "byteLength": 4096}]},
    {"entityId": "tmdb:episode:show:1399:s1:e2", "entityKind": "series", "title": "Game of Thrones", "year": 2011,
     "sources": [{"publicationId": "pub-got", "publisherId": "0123", "renditionId": "rend-2", "byteLength": 2048}]},
    {"entityId": "tmdb:movie:604", "entityKind": "movie", "title": "The Matrix Reloaded", "year": 2003,
     "sources": [{"publicationId": "pub-reloaded", "publisherId": "fedcba9876543210", "renditionId": "rend-3", "byteLength": 8192}]},
    {"entityId": "tmdb:movie:605", "entityKind": "movie", "title": "No Rendition", "year": 2020,
     "sources": [{"publicationId": "pub-broken", "publisherId": "aaaa", "renditionId": "", "byteLength": 10}]}
  ],
  "nextCursor": null
}`

type stubRelay struct {
	server        *httptest.Server
	catalogCalls  int
	archiveFields map[string]string
	archiveBytes  []byte
	archiveName   string
}

func newStubRelay(t *testing.T) (*stubRelay, *Client) {
	t.Helper()
	stub := &stubRelay{archiveFields: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		stub.catalogCalls++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, catalogBody)
	})
	mux.HandleFunc("/api/v1/archive", func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			body, _ := io.ReadAll(part)
			if part.FileName() != "" {
				stub.archiveName = part.FileName()
				stub.archiveBytes = body
				continue
			}
			stub.archiveFields[part.FormName()] = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(ArchiveJob{JobID: "job-1", Status: "queued", EntityHint: "movie:603"})
	})
	mux.HandleFunc("/api/v1/archive/missing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"JOB_NOT_FOUND","message":"no such job","field":"jobId"}}`)
	})
	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)

	client, err := New(stub.server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return stub, client
}

func TestSearchMatchesMovieByTMDBCoordinates(t *testing.T) {
	_, client := newStubRelay(t)

	results, err := client.Search(context.Background(), SearchRequest{Title: "irrelevant", TMDBID: "603"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(results), results)
	}
	got := results[0]
	if got.ServiceType != models.ServiceTypeP2P {
		t.Fatalf("serviceType = %q", got.ServiceType)
	}
	want := client.BaseURL() + "/api/v1/stream/pub-matrix/rend-1"
	if got.DownloadURL != want || got.Link != want || got.Attributes["stream_url"] != want {
		t.Fatalf("stream URL = %q / %q / %q, want %q", got.DownloadURL, got.Link, got.Attributes["stream_url"], want)
	}
	if got.SizeBytes != 4096 {
		t.Fatalf("sizeBytes = %d", got.SizeBytes)
	}
	if got.Indexer != IndexerName || got.GUID != "peartube:pub-matrix:rend-1" {
		t.Fatalf("indexer=%q guid=%q", got.Indexer, got.GUID)
	}
	// Nothing about seeders, cache state, or resolution is knowable from a
	// catalog entry, so none of it may appear.
	for _, forbidden := range []string{"seeders", "cached", "resolution"} {
		if _, ok := got.Attributes[forbidden]; ok {
			t.Fatalf("result fabricated a %q attribute: %+v", forbidden, got.Attributes)
		}
	}
}

func TestSearchMatchesEpisodeCoordinates(t *testing.T) {
	_, client := newStubRelay(t)

	results, err := client.Search(context.Background(), SearchRequest{Title: "Game of Thrones", Season: 1, Episode: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Title, "S01E02") {
		t.Fatalf("title = %q, expected episode coordinates", results[0].Title)
	}

	wrong, err := client.Search(context.Background(), SearchRequest{Title: "Game of Thrones", Season: 1, Episode: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(wrong) != 0 {
		t.Fatalf("a request for s01e03 matched %d sources", len(wrong))
	}
}

func TestSearchFallsBackToTitleAndYear(t *testing.T) {
	_, client := newStubRelay(t)

	// Punctuation and case drift must not defeat the match, and the year is
	// allowed one year of tolerance.
	results, err := client.Search(context.Background(), SearchRequest{Title: "the matrix!", Year: 2000})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Attributes["publicationId"] != "pub-matrix" {
		t.Fatalf("unexpected results: %+v", results)
	}

	// "The Matrix" must not match "The Matrix Reloaded".
	if len(results) == 1 && strings.Contains(results[0].Title, "Reloaded") {
		t.Fatal("title match was too loose")
	}

	far, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix", Year: 2010})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(far) != 0 {
		t.Fatalf("a 11-year year gap matched %d sources", len(far))
	}
}

func TestSearchSkipsSourcesWithoutARendition(t *testing.T) {
	_, client := newStubRelay(t)

	results, err := client.Search(context.Background(), SearchRequest{Title: "No Rendition", Year: 2020})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("an unaddressable source was offered: %+v", results)
	}
}

func TestCatalogIsCachedAcrossSearches(t *testing.T) {
	stub, client := newStubRelay(t)

	for range 3 {
		if _, err := client.Search(context.Background(), SearchRequest{TMDBID: "603"}); err != nil {
			t.Fatalf("Search: %v", err)
		}
	}
	if stub.catalogCalls != 1 {
		t.Fatalf("catalog fetched %d times, expected 1", stub.catalogCalls)
	}
}

func TestResolvePlaybackReturnsRelayStreamURL(t *testing.T) {
	_, client := newStubRelay(t)

	results, err := client.Search(context.Background(), SearchRequest{TMDBID: "603"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	resolution, err := ResolvePlayback(client, results[0])
	if err != nil {
		t.Fatalf("ResolvePlayback: %v", err)
	}
	if resolution.WebDAVPath != client.BaseURL()+"/api/v1/stream/pub-matrix/rend-1" {
		t.Fatalf("webdavPath = %q", resolution.WebDAVPath)
	}
	if resolution.HealthStatus != HealthStatus {
		t.Fatalf("healthStatus = %q, want %q", resolution.HealthStatus, HealthStatus)
	}
	if resolution.FileSize != 4096 {
		t.Fatalf("fileSize = %d", resolution.FileSize)
	}
}

func TestResolvePlaybackRejectsForeignStreamURL(t *testing.T) {
	_, client := newStubRelay(t)

	_, err := ResolvePlayback(client, models.NZBResult{
		ServiceType: models.ServiceTypeP2P,
		Attributes:  map[string]string{"stream_url": "http://169.254.169.254/latest/meta-data"},
	})
	if err == nil {
		t.Fatal("a stream URL pointing away from the relay was accepted")
	}
}

func TestArchiveStreamsFileAndCoordinates(t *testing.T) {
	stub, client := newStubRelay(t)

	path := filepath.Join(t.TempDir(), "The.Matrix.mkv")
	if err := os.WriteFile(path, []byte("media-bytes"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	job, err := client.Archive(context.Background(), ArchiveRequest{
		FilePath:    path,
		ContentKind: "movie",
		TMDBID:      "603",
		TMDBTitle:   "The Matrix",
		TMDBYear:    1999,
		Runtime:     136,
		Genres:      "Action,Science Fiction",
	})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if job.JobID != "job-1" || job.Status != "queued" || job.EntityHint != "movie:603" {
		t.Fatalf("job = %+v", job)
	}
	if string(stub.archiveBytes) != "media-bytes" || stub.archiveName != "The.Matrix.mkv" {
		t.Fatalf("uploaded %q as %q", stub.archiveBytes, stub.archiveName)
	}
	for field, want := range map[string]string{
		"contentKind": "movie",
		"tmdbId":      "603",
		"tmdbTitle":   "The Matrix",
		"tmdbYear":    "1999",
		"tmdbRuntime": "136",
		"tmdbGenres":  "Action,Science Fiction",
	} {
		if stub.archiveFields[field] != want {
			t.Fatalf("field %s = %q, want %q", field, stub.archiveFields[field], want)
		}
	}
	if _, ok := stub.archiveFields["tmdbSeason"]; ok {
		t.Fatal("a movie upload carried season coordinates")
	}
}

func TestArchiveRejectsIncompleteEpisodeCoordinates(t *testing.T) {
	_, client := newStubRelay(t)

	_, err := client.Archive(context.Background(), ArchiveRequest{
		FilePath:    filepath.Join(t.TempDir(), "missing.mkv"),
		ContentKind: "episode",
		TMDBID:      "1399",
		TMDBTitle:   "Game of Thrones",
		TMDBSeason:  1,
	})
	if err == nil {
		t.Fatal("an episode without an episode number was accepted")
	}
}

func TestArchiveStatusSurfacesRelayError(t *testing.T) {
	_, client := newStubRelay(t)

	_, err := client.ArchiveStatus(context.Background(), "missing")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error = %T (%v), want *APIError", err, err)
	}
	if apiErr.Status != http.StatusNotFound || apiErr.Code != "JOB_NOT_FOUND" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

func TestEnvGatingKeepsIntegrationInert(t *testing.T) {
	empty := func(string) string { return "" }
	if client, err := newFromEnv(empty); client != nil || err != nil {
		t.Fatalf("unset env produced client=%v err=%v", client, err)
	}

	enabled := func(key string) string {
		if key == EnabledEnv {
			return "true"
		}
		return ""
	}
	client, err := newFromEnv(enabled)
	if err != nil {
		t.Fatalf("newFromEnv: %v", err)
	}
	if client == nil || client.BaseURL() != DefaultRelayURL {
		t.Fatalf("enabled without a URL gave %v", client)
	}

	off := func(key string) string {
		switch key {
		case EnabledEnv:
			return "false"
		case RelayURLEnv:
			return "http://relay.invalid:8178"
		}
		return ""
	}
	if client, _ := newFromEnv(off); client != nil {
		t.Fatal("an explicit disable was overridden by a configured URL")
	}
}
