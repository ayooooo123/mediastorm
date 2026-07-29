package peartube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
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
	archiveCalls  int
	archiveFields map[string]string
	archiveBytes  []byte
	archiveName   string
	// archiveJSON is the decoded body of a URL seed, and archiveRefusal is a
	// verbatim error envelope the relay answers a URL seed with instead of 202.
	archiveJSON    map[string]any
	archiveRefusal string
}

// gateBody is verbatim what a relay bound to a non-loopback address answers
// when the operator never passed --api-open.
const gateBody = `{"error":{"code":"OPEN_ACCESS_NOT_ENABLED","message":"the relay is bound to 0.0.0.0 rather than loopback, so /api/v1/catalog and /api/v1/stream refuse to enumerate or serve media; restart the relay with --api-open (or PEARTUBE_ARCHIVE_API_OPEN=1)","field":null}}`

func (stub *stubRelay) handleArchive(w http.ResponseWriter, r *http.Request) {
	stub.archiveCalls++
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		stub.archiveJSON = map[string]any{}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &stub.archiveJSON); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if stub.archiveRefusal != "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, stub.archiveRefusal)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(ArchiveJob{JobID: "arch_0123456789abcdef", Status: "queued", EntityHint: "movie:9522"})
		return
	}
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
}

func newRelay(t *testing.T, catalog http.HandlerFunc) (*stubRelay, *Client) {
	t.Helper()
	stub := &stubRelay{archiveFields: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		stub.catalogCalls++
		catalog(w, r)
	})
	mux.HandleFunc("/api/v1/archive", stub.handleArchive)
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

func newStubRelay(t *testing.T) (*stubRelay, *Client) {
	t.Helper()
	return newRelay(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, catalogBody)
	})
}

// newGatedRelay is a relay the operator has not opted into open access on: it
// refuses to enumerate or serve media, but still accepts seeds.
func newGatedRelay(t *testing.T) (*stubRelay, *Client) {
	t.Helper()
	return newRelay(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, gateBody)
	})
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
		FilePath: path,
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "movie",
			TMDBID:      "603",
			TMDBTitle:   "The Matrix",
			TMDBYear:    1999,
			Runtime:     136,
			Genres:      "Action,Science Fiction",
		},
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
		FilePath: filepath.Join(t.TempDir(), "missing.mkv"),
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "episode",
			TMDBID:      "1399",
			TMDBTitle:   "Game of Thrones",
			TMDBSeason:  1,
		},
	})
	if err == nil {
		t.Fatal("an episode without an episode number was accepted")
	}
}

// A debrid or usenet stream is only seedable because the relay fetches the URL
// itself: this backend never holds those bytes.
func TestArchiveURLSendsMovieCoordinatesWithoutEpisodeFields(t *testing.T) {
	stub, client := newStubRelay(t)

	job, err := client.ArchiveURL(context.Background(), ArchiveURLRequest{
		SourceURL: "https://cdn.example.net/d/TOKEN/Wedding.Crashers.2005.mkv",
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "movie",
			TMDBID:      "9522",
			TMDBTitle:   "Wedding Crashers",
			TMDBYear:    2005,
			Runtime:     119,
			Genres:      "Comedy,Romance",
		},
	})
	if err != nil {
		t.Fatalf("ArchiveURL: %v", err)
	}
	if job.JobID != "arch_0123456789abcdef" || job.Status != "queued" || job.EntityHint != "movie:9522" {
		t.Fatalf("job = %+v", job)
	}
	if stub.archiveBytes != nil {
		t.Fatalf("a URL seed uploaded %d bytes", len(stub.archiveBytes))
	}
	for field, want := range map[string]any{
		"url":         "https://cdn.example.net/d/TOKEN/Wedding.Crashers.2005.mkv",
		"contentKind": "movie",
		"tmdbId":      "9522",
		"tmdbTitle":   "Wedding Crashers",
		"tmdbYear":    "2005",
		"tmdbRuntime": "119",
		"tmdbGenres":  "Comedy,Romance",
	} {
		if stub.archiveJSON[field] != want {
			t.Fatalf("field %s = %#v, want %#v", field, stub.archiveJSON[field], want)
		}
	}
	// The relay rejects season/episode on a movie outright, so they must be
	// absent rather than zero.
	for _, field := range []string{"tmdbSeason", "tmdbEpisode"} {
		if _, ok := stub.archiveJSON[field]; ok {
			t.Fatalf("a movie URL seed carried %s", field)
		}
	}
}

func TestArchiveURLSendsEpisodeCoordinates(t *testing.T) {
	stub, client := newStubRelay(t)

	if _, err := client.ArchiveURL(context.Background(), ArchiveURLRequest{
		SourceURL: "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv",
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "episode",
			TMDBID:      "1399",
			TMDBTitle:   "Game of Thrones",
			TMDBSeason:  1,
			TMDBEpisode: 2,
		},
	}); err != nil {
		t.Fatalf("ArchiveURL: %v", err)
	}
	if stub.archiveJSON["tmdbSeason"] != float64(1) || stub.archiveJSON["tmdbEpisode"] != float64(2) {
		t.Fatalf("season/episode = %#v/%#v", stub.archiveJSON["tmdbSeason"], stub.archiveJSON["tmdbEpisode"])
	}
	if stub.archiveJSON["contentKind"] != "episode" {
		t.Fatalf("contentKind = %#v", stub.archiveJSON["contentKind"])
	}
}

// A URL the relay will not fetch has to be distinguishable from a relay that is
// broken: only the first is fixed by supplying a different source.
func TestArchiveURLSurfacesARefusedSource(t *testing.T) {
	stub, client := newStubRelay(t)
	stub.archiveRefusal = `{"error":{"code":"SOURCE_HOST_NOT_PUBLIC","message":"source host 10.0.0.5 is not publicly routable","field":"url"}}`

	_, err := client.ArchiveURL(context.Background(), ArchiveURLRequest{
		SourceURL: "https://10.0.0.5/movie.mkv",
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "movie",
			TMDBID:      "9522",
			TMDBTitle:   "Wedding Crashers",
		},
	})
	if !IsSourceRefused(err) {
		t.Fatalf("error = %v, want a refused source", err)
	}
	if IsRelayNotOpen(err) {
		t.Fatalf("a refused source was mistaken for the open-access gate: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T (%v), want *APIError", err, err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "SOURCE_HOST_NOT_PUBLIC" || apiErr.Field != "url" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

// Every other relay failure must stay outside the sentinel, or "give me a
// different URL" becomes the advice for problems a different URL cannot fix.
func TestArchiveURLDoesNotTreatEveryRefusalAsASourceProblem(t *testing.T) {
	stub, client := newStubRelay(t)
	stub.archiveRefusal = `{"error":{"code":"UPLOAD_DIR_UNAVAILABLE","message":"the relay cannot write to its upload directory","field":null}}`

	_, err := client.ArchiveURL(context.Background(), ArchiveURLRequest{
		SourceURL: "https://cdn.example.net/d/TOKEN/movie.mkv",
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "movie",
			TMDBID:      "9522",
			TMDBTitle:   "Wedding Crashers",
		},
	})
	if err == nil {
		t.Fatal("a failing relay was reported as success")
	}
	if IsSourceRefused(err) {
		t.Fatalf("a relay-side failure was blamed on the source URL: %v", err)
	}
}

// An obvious mistake must not cost a round trip.
func TestArchiveURLValidatesBeforeReachingTheRelay(t *testing.T) {
	for name, req := range map[string]ArchiveURLRequest{
		"no url": {ArchiveCoordinates: ArchiveCoordinates{ContentKind: "movie", TMDBID: "9522", TMDBTitle: "Wedding Crashers"}},
		"relative url": {
			SourceURL:          "/debrid/torbox/12345/file/9",
			ArchiveCoordinates: ArchiveCoordinates{ContentKind: "movie", TMDBID: "9522", TMDBTitle: "Wedding Crashers"},
		},
		"episode without an episode number": {
			SourceURL:          "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv",
			ArchiveCoordinates: ArchiveCoordinates{ContentKind: "episode", TMDBID: "1399", TMDBTitle: "Game of Thrones", TMDBSeason: 1},
		},
		"movie carrying episode coordinates": {
			SourceURL:          "https://cdn.example.net/d/TOKEN/movie.mkv",
			ArchiveCoordinates: ArchiveCoordinates{ContentKind: "movie", TMDBID: "9522", TMDBTitle: "Wedding Crashers", TMDBSeason: 1, TMDBEpisode: 1},
		},
		"unknown content kind": {
			SourceURL:          "https://cdn.example.net/d/TOKEN/movie.mkv",
			ArchiveCoordinates: ArchiveCoordinates{ContentKind: "season", TMDBID: "9522", TMDBTitle: "Wedding Crashers"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub, client := newStubRelay(t)
			if _, err := client.ArchiveURL(context.Background(), req); err == nil {
				t.Fatal("accepted")
			}
			if stub.archiveCalls != 0 {
				t.Fatalf("the relay was contacted %d times", stub.archiveCalls)
			}
		})
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

// The environment alone must still gate the integration exactly as it did
// before the admin settings existed: nothing set means no relay at all.
func TestEnvGatingKeepsIntegrationInert(t *testing.T) {
	empty := func(string) string { return "" }
	if resolved := resolve(config.PearTubeSettings{}, empty); resolved.RelayURL != "" || resolved.Enabled {
		t.Fatalf("unset env produced %+v", resolved)
	}

	enabled := func(key string) string {
		if key == EnabledEnv {
			return "true"
		}
		return ""
	}
	resolved := resolve(config.PearTubeSettings{}, enabled)
	if resolved.RelayURL != DefaultRelayURL || !resolved.Enabled || !resolved.EnabledFromEnv {
		t.Fatalf("enabled without a URL gave %+v", resolved)
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
	if resolved := resolve(config.PearTubeSettings{}, off); resolved.RelayURL != "" || resolved.Enabled {
		t.Fatalf("an explicit disable was overridden by a configured URL: %+v", resolved)
	}
}

// A relay that has not been opted into open access must be identifiable as
// exactly that — not as a dead relay, and not as an empty catalog.
func TestSearchOnGatedRelayYieldsSentinelAndNoResults(t *testing.T) {
	_, client := newGatedRelay(t)

	results, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix", TMDBID: "603"})
	if len(results) != 0 {
		t.Fatalf("gated relay produced %d results: %+v", len(results), results)
	}
	if !IsRelayNotOpen(err) {
		t.Fatalf("error = %v, want it to match ErrRelayNotOpen", err)
	}
	if !errors.Is(err, ErrRelayNotOpen) {
		t.Fatalf("errors.Is(%v, ErrRelayNotOpen) = false", err)
	}
	// The code is what identifies the gate; the message varies with the bind
	// address, so nothing may depend on it.
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T, want it to carry an *APIError", err)
	}
	if apiErr.Status != http.StatusForbidden || apiErr.Code != "OPEN_ACCESS_NOT_ENABLED" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if !strings.Contains(ErrRelayNotOpen.Error(), "--api-open") ||
		!strings.Contains(ErrRelayNotOpen.Error(), "PEARTUBE_ARCHIVE_API_OPEN=1") {
		t.Fatalf("sentinel does not carry the remedy: %v", ErrRelayNotOpen)
	}
}

// A different 403, or any other relay failure, must not be mistaken for the
// gate: those are not fixed by --api-open.
func TestUnrelatedRelayErrorIsNotTheGate(t *testing.T) {
	_, client := newRelay(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":"FORBIDDEN","message":"nope","field":null}}`)
	})

	_, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix"})
	if err == nil {
		t.Fatal("a 403 FORBIDDEN was swallowed")
	}
	if IsRelayNotOpen(err) {
		t.Fatalf("error %v was misread as the open-access gate", err)
	}
}

// The gate must be reported once, not once per search: a stream search runs on
// every play attempt. The catalog cache is bypassed here so the guard, and not
// the cache, is what does the suppressing.
func TestGateIsLoggedOncePerOccurrence(t *testing.T) {
	_, client := newGatedRelay(t)

	var logged strings.Builder
	restore := log.Writer()
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(restore) })

	for range 5 {
		client.mu.Lock()
		client.cachedAt = time.Time{}
		client.mu.Unlock()
		if _, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix"}); !IsRelayNotOpen(err) {
			t.Fatalf("Search error = %v", err)
		}
	}

	warnings := strings.Count(logged.String(), "WARN: relay "+client.BaseURL())
	if warnings != 1 {
		t.Fatalf("gate logged %d times across 5 searches:\n%s", warnings, logged.String())
	}
	if !strings.Contains(logged.String(), NotOpenRemedy) {
		t.Fatalf("gate warning omitted the remedy:\n%s", logged.String())
	}
}

// Seeding is not gated on the relay side. That asymmetry is deliberate: a
// container-hosted backend can publish into the swarm before the operator
// decides to let this network read from it.
func TestSeedingSucceedsAgainstAGatedRelay(t *testing.T) {
	stub, client := newGatedRelay(t)

	if _, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix"}); !IsRelayNotOpen(err) {
		t.Fatalf("expected the catalog to be gated, got %v", err)
	}

	path := filepath.Join(t.TempDir(), "The.Matrix.mkv")
	if err := os.WriteFile(path, []byte("media-bytes"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	job, err := client.Archive(context.Background(), ArchiveRequest{
		FilePath: path,
		ArchiveCoordinates: ArchiveCoordinates{
			ContentKind: "movie",
			TMDBID:      "603",
			TMDBTitle:   "The Matrix",
			TMDBYear:    1999,
		},
	})
	if err != nil {
		t.Fatalf("Archive against a gated relay: %v", err)
	}
	if job.JobID != "job-1" || job.Status != "queued" {
		t.Fatalf("job = %+v", job)
	}
	if string(stub.archiveBytes) != "media-bytes" {
		t.Fatalf("uploaded %q", stub.archiveBytes)
	}
}

func TestProbeSeparatesGatedFromReadyAndUnreachable(t *testing.T) {
	_, gated := newGatedRelay(t)
	state := gated.Probe(context.Background())
	if !state.Reachable || !state.NotOpen || !state.SeedingAvailable {
		t.Fatalf("gated state = %+v, want reachable+notOpen+seedingAvailable", state)
	}
	if state.Remedy != NotOpenRemedy {
		t.Fatalf("remedy = %q, want %q", state.Remedy, NotOpenRemedy)
	}
	if !strings.Contains(state.Detail, "0.0.0.0") {
		t.Fatalf("detail lost the relay's own explanation: %q", state.Detail)
	}
	if state.CatalogEntities != 0 {
		t.Fatalf("gated relay reported %d entities", state.CatalogEntities)
	}

	_, healthy := newStubRelay(t)
	ready := healthy.Probe(context.Background())
	if !ready.Reachable || ready.NotOpen || ready.CatalogEntities != 4 {
		t.Fatalf("ready state = %+v", ready)
	}

	dead, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	down := dead.Probe(context.Background())
	if down.Reachable || down.NotOpen || down.SeedingAvailable {
		t.Fatalf("unreachable state = %+v", down)
	}
}
