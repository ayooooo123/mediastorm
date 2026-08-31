package peartube

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

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
	server                *httptest.Server
	catalogCalls          int
	archiveCalls          int
	archiveFields         map[string]string
	archiveBytes          []byte
	archiveName           string
	archiveIdempotencyKey string
}

// gateBody is verbatim what a relay bound to a non-loopback address answers
// when the operator never passed --api-open.
const gateBody = `{"error":{"code":"OPEN_ACCESS_NOT_ENABLED","message":"the relay is bound to 0.0.0.0 rather than loopback, so /api/v1/catalog and /api/v1/stream refuse to enumerate or serve media; restart the relay with --api-open (or PEARTUBE_ARCHIVE_API_OPEN=1)","field":null}}`

func (stub *stubRelay) handleArchive(w http.ResponseWriter, r *http.Request) {
	stub.archiveCalls++
	stub.archiveIdempotencyKey = r.Header.Get("Idempotency-Key")
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

const (
	companionTestSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	candidateRefA       = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	candidateRefB       = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

func newCompanionSearchClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	t.Setenv(CompanionClientEnv, "mediastorm-test")
	t.Setenv(CompanionSharedSecretEnv, companionTestSecret)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func assertCompanionAuth(t *testing.T, r *http.Request) {
	t.Helper()
	assertCompanionAuthBody(t, r, nil)
}

func assertCompanionAuthBody(t *testing.T, r *http.Request, body []byte) {
	t.Helper()
	if got := r.Header.Get("X-PearTube-Client"); got != "mediastorm-test" {
		t.Errorf("X-PearTube-Client = %q", got)
	}
	timestamp := r.Header.Get("X-PearTube-Timestamp")
	if parsed, err := strconv.ParseInt(timestamp, 10, 64); err != nil || parsed <= 0 {
		t.Errorf("X-PearTube-Timestamp = %q", timestamp)
	}
	nonce := r.Header.Get("X-PearTube-Nonce")
	if len(nonce) < 8 || len(nonce) > 128 {
		t.Errorf("X-PearTube-Nonce length = %d", len(nonce))
	}

	target := r.URL.EscapedPath()
	if query := r.URL.Query().Encode(); query != "" {
		target += "?" + query
	}
	bodyHash := blake2b.Sum256(body)
	canonical := strings.Join([]string{
		r.Method,
		target,
		timestamp,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	key, err := hex.DecodeString(companionTestSecret)
	if err != nil {
		t.Errorf("decode test secret: %v", err)
		return
	}
	mac := hmac.New(sha512.New, key)
	_, _ = mac.Write([]byte(canonical))
	want := mac.Sum(nil)
	provided, err := hex.DecodeString(r.Header.Get("X-PearTube-MAC"))
	if err != nil || !hmac.Equal(provided, want[:32]) {
		t.Errorf("X-PearTube-MAC did not authenticate canonical request %q", canonical)
	}
}

func TestApplyNetworkPolicyUsesAuthenticatedCompleteControlSnapshot(t *testing.T) {
	policy := CompanionNetworkPolicy{
		PolicyVersion:           2,
		ConsentVersion:          1,
		MigrationRequired:       false,
		ContributeWatchedMedia:  true,
		ArchiveEnabled:          false,
		ContributionBudgetBytes: 4 * 1024 * 1024 * 1024,
		ArchiveBudgetBytes:      8 * 1024 * 1024 * 1024,
		UploadPermission:        "enabled",
		UploadCeilingBytes:      4 * 1024 * 1024 * 1024,
	}
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/v2/policy" {
			t.Fatalf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read policy body: %v", err)
		}
		assertCompanionAuthBody(t, r, body)
		var got CompanionNetworkPolicy
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode policy body: %v", err)
		}
		if got != policy {
			t.Fatalf("policy = %#v, want %#v", got, policy)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"policy":{"policyVersion":2}}`)
	})
	if err := client.ApplyNetworkPolicy(context.Background(), policy); err != nil {
		t.Fatalf("ApplyNetworkPolicy: %v", err)
	}
}

func companionCandidate(ref, title string) map[string]any {
	return map[string]any{
		"schemaVersion": 2,
		"candidateRef":  ref,
		"work": map[string]any{
			"title":       title,
			"releaseYear": 1999,
		},
		"publication": map[string]any{
			"publicationId": "publication-" + ref[:1],
			"publisherId":   "publisher-" + ref[:1],
		},
		"rendition": map[string]any{
			"renditionId":     "rendition-" + ref[:1],
			"container":       "video/mp4",
			"videoCodec":      "avc1",
			"width":           1920,
			"height":          1080,
			"resolutionLabel": "1080p",
			"byteLength":      4096,
		},
		"asset": map[string]any{
			"assetId":    "asset-" + ref[:1],
			"byteLength": 4096,
		},
		"availability": map[string]any{
			"peers":           7,
			"completeSeeders": 3,
			"observedAtMs":    1786406400000,
			"expiresAtMs":     1786406460000,
		},
		"streamUrl":    "https://forbidden.invalid/stream",
		"downloadLink": "magnet:?xt=urn:btih:forbidden",
	}
}

func writeCompanionCandidates(t *testing.T, w http.ResponseWriter, candidates []map[string]any) {
	t.Helper()
	if candidates == nil {
		candidates = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"candidates": candidates, "cursor": nil}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestSearchUsesAuthenticatedExactMovieQuery(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.RequestURI(), "/api/v2/search?identifier=603&kind=movie&limit=7&namespace=tmdb"; got != want {
			t.Errorf("request target = %q, want %q", got, want)
		}
		assertCompanionAuth(t, r)
		writeCompanionCandidates(t, w, []map[string]any{companionCandidate(candidateRefA, "The Matrix")})
	})

	candidates, err := client.Search(context.Background(), SearchRequest{
		Title: "The Matrix", Year: 1999, MediaType: "movie", TMDBID: "603", MaxResults: 7,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(candidates) != 1 || candidates[0].CandidateRef != candidateRefA {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestSearchUsesExactEpisodeQueryFields(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.RequestURI(), "/api/v2/search?episode=2&identifier=1399&kind=episode&namespace=tmdb&season=1"; got != want {
			t.Errorf("request target = %q, want %q", got, want)
		}
		assertCompanionAuth(t, r)
		writeCompanionCandidates(t, w, nil)
	})

	_, err := client.Search(context.Background(), SearchRequest{
		Title: "Wrong.Show.S09E09", MediaType: "series", TMDBID: "1399", Season: 1, Episode: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearchEncodesFallbackTitleAndYear(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.RequestURI(), "/api/v2/search?kind=movie&title=The+Matrix+%26+Friends&year=1999"; got != want {
			t.Errorf("request target = %q, want %q", got, want)
		}
		writeCompanionCandidates(t, w, nil)
	})

	if _, err := client.Search(context.Background(), SearchRequest{
		Title: "The Matrix & Friends", Year: 1999, MediaType: "movie",
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestCompanionSearchTargetAndMACMatchPlan08Vector(t *testing.T) {
	target, _, err := (SearchRequest{
		Title: "M*A*S*H ~", Year: 1972, MediaType: "movie",
	}).companionSearchTarget()
	if err != nil {
		t.Fatalf("companionSearchTarget: %v", err)
	}
	// Pinned to packages/cli/src/companion/auth.js under the Plan 08 Node
	// runtime, whose URLSearchParams serializer differs from Go's QueryEscape.
	const wantTarget = "/api/v2/search?kind=movie&title=M*A*S*H+%7E&year=1972"
	if target != wantTarget {
		t.Fatalf("target = %q, want Plan 08 WHATWG target %q", target, wantTarget)
	}

	key, err := hex.DecodeString(companionTestSecret)
	if err != nil {
		t.Fatalf("decode test key: %v", err)
	}
	gotMAC := companionRequestMAC(
		http.MethodGet,
		target,
		"1786406400000",
		"review-vector-0001",
		key,
	)
	const wantMAC = "0257e4c23b06122feaf0a3faaefb370dabd11d067b9df9efc4c18c9777cf044b"
	if gotMAC != wantMAC {
		t.Fatalf("MAC = %q, want Plan 08 signer vector %q", gotMAC, wantMAC)
	}
}

func TestMapCandidatesReturnsDeterministicURLLessPearTubeResults(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeCompanionCandidates(t, w, []map[string]any{
			companionCandidate(candidateRefA, "The Matrix"),
			companionCandidate(candidateRefB, "The Matrix"),
		})
	})
	candidates, err := client.Search(context.Background(), SearchRequest{
		Title: "The Matrix", Year: 1999, MediaType: "movie",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	results := MapCandidates(SearchRequest{Title: "The Matrix", Year: 1999, MediaType: "movie"}, candidates)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	got := results[0]
	if got.ServiceType != models.ServiceTypePearTube {
		t.Fatalf("service type = %q", got.ServiceType)
	}
	if got.Link != "" || got.DownloadURL != "" {
		t.Fatal("search minted a playback URL")
	}
	if got.Attributes["peartube_candidate_ref"] != candidateRefA {
		t.Fatal("missing deferred candidate reference")
	}
	if got.Attributes["preresolved"] != "" {
		t.Fatal("PearTube was routed through debrid")
	}
	if got.Attributes["resolution"] != "1080p" ||
		got.Attributes["videoCodec"] != "avc1" ||
		got.Attributes["seeders"] != "3" ||
		got.Attributes["peers"] != "7" {
		t.Fatalf("factual attributes = %+v", got.Attributes)
	}
	if got.SizeBytes != 4096 {
		t.Fatalf("sizeBytes = %d", got.SizeBytes)
	}
	if results[0].GUID == results[1].GUID {
		t.Fatalf("multiple candidates share GUID %q", results[0].GUID)
	}
	serialized, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(serialized), "forbidden.invalid") ||
		strings.Contains(string(serialized), "magnet:") {
		t.Fatalf("result leaked a playback locator: %s", serialized)
	}
}

func TestSearchReturnsEmptyCandidates(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeCompanionCandidates(t, w, []map[string]any{})
	})
	candidates, err := client.Search(context.Background(), SearchRequest{Title: "Missing", MediaType: "movie"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestSearchRejectsMalformedCandidate(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		candidate := companionCandidate("short", "The Matrix")
		writeCompanionCandidates(t, w, []map[string]any{candidate})
	})
	if _, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix", MediaType: "movie"}); err == nil {
		t.Fatal("malformed candidate was accepted")
	}
}

func TestSearchBoundsCandidateResponse(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		candidate := companionCandidate(candidateRefA, strings.Repeat("x", 513))
		writeCompanionCandidates(t, w, []map[string]any{candidate})
	})
	if _, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix", MediaType: "movie"}); err == nil {
		t.Fatal("oversized candidate was accepted")
	}
}

func TestSearchReturnsMalformedEnvelopeError(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"cursor":null}`)
	})
	if _, err := client.Search(context.Background(), SearchRequest{Title: "The Matrix", MediaType: "movie"}); err == nil {
		t.Fatal("malformed response envelope was accepted")
	}
}

func TestSearchHonorsContextTimeout(t *testing.T) {
	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Search(ctx, SearchRequest{Title: "The Matrix", MediaType: "movie"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Search error = %v, want deadline exceeded", err)
	}
}

func TestSearchRefusesRedirectsWithoutForwardingAuth(t *testing.T) {
	var redirectedRequests atomic.Int64
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		if r.Header.Get("X-PearTube-MAC") != "" {
			t.Error("authenticated companion headers reached redirect target")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(redirectTarget.Close)

	client := newCompanionSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertCompanionAuth(t, r)
		http.Redirect(w, r, redirectTarget.URL+"/capture", http.StatusTemporaryRedirect)
	})
	if _, err := client.Search(context.Background(), SearchRequest{
		Title: "The Matrix", MediaType: "movie",
	}); err == nil {
		t.Fatal("authenticated companion redirect was accepted")
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d request(s), want 0", got)
	}
}

func TestResolverReturnsDirectOwnedCapabilityURLConsumedWithoutControlHeadersOrRedirects(t *testing.T) {
	t.Setenv(CompanionClientEnv, "mediastorm-test")
	t.Setenv(CompanionSharedSecretEnv, companionTestSecret)
	const capability = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	var streamRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/proxy/api/v2/streams/open":
			if r.Method != http.MethodPost {
				t.Errorf("open method = %s", r.Method)
			}
			for _, header := range []string{"X-PearTube-Client", "X-PearTube-Timestamp", "X-PearTube-Nonce", "X-PearTube-MAC"} {
				if r.Header.Get(header) == "" {
					t.Errorf("open request omitted %s", header)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"url":"/proxy/api/v2/stream/pub%3A1/rend%3A1?cap=`+capability+`","expiresAt":1786406460000,"publicationId":"pub:1","renditionId":"rend:1"}`)
		case "/proxy/api/v2/stream/pub%3A1/rend%3A1":
			streamRequests.Add(1)
			for _, header := range []string{"Authorization", "X-PearTube-Client", "X-PearTube-Timestamp", "X-PearTube-Nonce", "X-PearTube-MAC"} {
				if r.Header.Get(header) != "" {
					t.Errorf("capability stream request carried control header %s", header)
				}
			}
			if got := r.URL.Query()["cap"]; len(got) != 1 || got[0] != capability {
				t.Errorf("stream capability = %v", got)
			}
			if r.Header.Get("Range") != "bytes=1-3" {
				t.Errorf("Range = %q", r.Header.Get("Range"))
			}
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", "bytes 1-3/5")
			w.Header().Set("Content-Length", "3")
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, "edi")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	Configure(Resolved{RelayURL: server.URL + "/proxy", Enabled: true})
	t.Cleanup(func() { Configure(Resolved{}) })

	resolution, err := (&Resolver{}).Open(context.Background(), candidateRefA)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, resolution.WebDAVPath, nil)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	request.Header.Set("Range", "bytes=1-3")
	redirects := 0
	response, err := (&http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			redirects++
			return http.ErrUseLastResponse
		},
	}).Do(request)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if response.StatusCode != http.StatusPartialContent || string(body) != "edi" {
		t.Fatalf("stream response = %d %q", response.StatusCode, body)
	}
	if redirects != 0 {
		t.Fatalf("stream followed %d redirect(s)", redirects)
	}
	if streamRequests.Load() != 1 {
		t.Fatalf("stream requests = %d, want 1", streamRequests.Load())
	}
}

func TestOwnedCompanionStreamURLRejectsNonCanonicalCapabilityQuery(t *testing.T) {
	const capability = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	opened := companionOpenResponse{
		ExpiresAt:     1786406460000,
		PublicationID: "pub:1",
		RenditionID:   "rend:1",
	}
	for _, rawURL := range []string{
		"/api/v2/stream/pub%3A1/rend%3A1?cap=" + capability + "&",
		"/api/v2/stream/pub%3A1/rend%3A1?cap=%43" + capability[1:],
	} {
		opened.URL = rawURL
		if owned, err := ownedCompanionStreamURL("http://127.0.0.1:8174", opened); err == nil {
			t.Fatalf("ownedCompanionStreamURL(%q) = %q, want rejection", rawURL, owned)
		}
	}
}

func TestOwnedCompanionStreamURLBindsConfiguredBasePath(t *testing.T) {
	const capability = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	opened := companionOpenResponse{
		URL:           "/proxy/api/v2/stream/pub%3A1/rend%3A1?cap=" + capability,
		ExpiresAt:     1786406460000,
		PublicationID: "pub:1",
		RenditionID:   "rend:1",
	}
	const baseURL = "http://127.0.0.1:8174/proxy"
	owned, err := ownedCompanionStreamURL(baseURL, opened)
	if err != nil {
		t.Fatalf("ownedCompanionStreamURL: %v", err)
	}
	if want := baseURL + "/api/v2/stream/pub%3A1/rend%3A1?cap=" + capability; owned != want {
		t.Fatalf("owned URL = %q, want %q", owned, want)
	}

	opened.URL = "/api/v2/stream/pub%3A1/rend%3A1?cap=" + capability
	if owned, err := ownedCompanionStreamURL(baseURL, opened); err == nil {
		t.Fatalf("same-origin root stream URL = %q, want rejection", owned)
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

// The v1 catalog remains available to archive/probe callers. A relay that has
// not opted into open access must still be identifiable there, but v2 search
// no longer routes through this sentinel.
func TestCatalogOnGatedRelayYieldsSentinelAndNoEntities(t *testing.T) {
	_, client := newGatedRelay(t)

	entities, err := client.Catalog(context.Background())
	if len(entities) != 0 {
		t.Fatalf("gated relay produced %d entities: %+v", len(entities), entities)
	}
	if !IsRelayNotOpen(err) {
		t.Fatalf("error = %v, want it to match ErrRelayNotOpen", err)
	}
	if !errors.Is(err, ErrRelayNotOpen) {
		t.Fatalf("errors.Is(%v, ErrRelayNotOpen) = false", err)
	}
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

// A different v1 catalog refusal is not the archive/probe open-access gate.
func TestUnrelatedCatalogErrorIsNotTheGate(t *testing.T) {
	_, client := newRelay(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":"FORBIDDEN","message":"nope","field":null}}`)
	})

	_, err := client.Catalog(context.Background())
	if err == nil {
		t.Fatal("a 403 FORBIDDEN was swallowed")
	}
	if IsRelayNotOpen(err) {
		t.Fatalf("error %v was misread as the open-access gate", err)
	}
}

// Repeated v1 catalog probes announce the archive/probe gate only once.
func TestCatalogGateIsLoggedOncePerOccurrence(t *testing.T) {
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
		if _, err := client.Catalog(context.Background()); !IsRelayNotOpen(err) {
			t.Fatalf("Catalog error = %v", err)
		}
	}

	warnings := strings.Count(logged.String(), "WARN: relay "+client.BaseURL())
	if warnings != 1 {
		t.Fatalf("gate logged %d times across 5 catalog probes:\n%s", warnings, logged.String())
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

	if _, err := client.Catalog(context.Background()); !IsRelayNotOpen(err) {
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

func TestSourceFileNameForUsesSourcePathBaseName(t *testing.T) {
	cases := []struct {
		name         string
		sourcePath   string
		releaseTitle string
		tmdbTitle    string
		coordinates  ArchiveCoordinates
		contentType  string
		want         string
	}{
		{
			name:         "explicit release title with codecs wins over obfuscated usenet stream path",
			sourcePath:   "/webdav/40d2ab32c8354b7e9466d7af14e4474b.mkv",
			releaseTitle: "Justice.League.Dark.Apokolips.War.2020.2160p.BDRip.AAC.5.1.HDR10.x265.10bit-MarkII",
			tmdbTitle:    "Justice League Dark: Apokolips War",
			coordinates:  ArchiveCoordinates{ContentKind: "movie", TMDBID: "618353"},
			contentType:  "video/x-matroska",
			want:         "Justice.League.Dark.Apokolips.War.2020.2160p.BDRip.AAC.5.1.HDR10.x265.10bit-MarkII.mkv",
		},
		{
			name:         "remote stream path base name wins over the work title",
			sourcePath:   "/torbox/cache/Rick.and.Morty.S01E11.1080p.WEB.h264.mkv",
			releaseTitle: "",
			tmdbTitle:    "Rick and Morty",
			coordinates:  ArchiveCoordinates{ContentKind: "episode", TMDBID: "60625", TMDBSeason: 1, TMDBEpisode: 11},
			contentType:  "video/x-matroska",
			want:         "Rick.and.Morty.S01E11.1080p.WEB.h264.mkv",
		},
		{
			name:         "season episode fallback when no path exists",
			sourcePath:   "",
			releaseTitle: "",
			tmdbTitle:    "Avatar: The Last Airbender",
			coordinates:  ArchiveCoordinates{ContentKind: "episode", TMDBID: "246", TMDBSeason: 2, TMDBEpisode: 2},
			contentType:  "video/x-matroska",
			want:         "Avatar: The Last Airbender S02E02.mkv",
		},
		{
			name:         "movie title with container when no path exists",
			sourcePath:   "",
			releaseTitle: "",
			tmdbTitle:    "Spider-Man: Into the Spider-Verse",
			coordinates:  ArchiveCoordinates{ContentKind: "movie", TMDBID: "324857"},
			contentType:  "video/mp4",
			want:         "Spider-Man: Into the Spider-Verse.mp4",
		},
		{
			name:         "bare container only when nothing is known",
			sourcePath:   "",
			releaseTitle: "",
			tmdbTitle:    "",
			coordinates:  ArchiveCoordinates{ContentKind: "movie"},
			contentType:  "",
			want:         "bin",
		},
		{
			name:         "path basename with directory components",
			sourcePath:   "/Users/jd/mediastorm-local/cache/streams/Show.Name.S03E08.720p.mkv",
			releaseTitle: "",
			tmdbTitle:    "Show Name",
			coordinates:  ArchiveCoordinates{ContentKind: "episode", TMDBSeason: 3, TMDBEpisode: 8},
			contentType:  "video/x-matroska",
			want:         "Show.Name.S03E08.720p.mkv",
		},
		{
			name:         "obfuscated usenet hex hash falls back to human title and container extension",
			sourcePath:   "/webdav/40d2ab32c8354b7e9466d7af14e4474b.mkv",
			releaseTitle: "",
			tmdbTitle:    "Justice League Dark: Apokolips War",
			coordinates:  ArchiveCoordinates{ContentKind: "movie", TMDBID: "618353"},
			contentType:  "video/x-matroska",
			want:         "Justice League Dark: Apokolips War.mkv",
		},
		{
			name:         "usenet unpacked directory name with timestamp prefix recovers full release tags",
			sourcePath:   "/1788205709466910000_Wrath.of.Man.2021.1080p.BluRay.H264.AAC/5295c8d8d367424f94feec38e0c1fbc8.mp4",
			releaseTitle: "",
			tmdbTitle:    "Wrath of Man",
			coordinates:  ArchiveCoordinates{ContentKind: "movie", TMDBID: "637649"},
			contentType:  "video/mp4",
			want:         "Wrath.of.Man.2021.1080p.BluRay.H264.AAC.mp4",
		},
		{
			name:         "usenet episode directory recovers full release tags",
			sourcePath:   "/webdav/1787969632207288000_Kim.Possible.S01E01.1080p.DSNP.WEB-DL.AAC2.0.H.264-PHOENiX/c08e883e1a374de083c795bc03bf23d2.mkv",
			releaseTitle: "",
			tmdbTitle:    "Kim Possible",
			coordinates:  ArchiveCoordinates{ContentKind: "episode", TMDBID: "123", TMDBSeason: 1, TMDBEpisode: 1},
			contentType:  "video/x-matroska",
			want:         "Kim.Possible.S01E01.1080p.DSNP.WEB-DL.AAC2.0.H.264-PHOENiX.mkv",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sourceFileNameFor(tc.sourcePath, tc.releaseTitle, tc.tmdbTitle, tc.coordinates, tc.contentType)
			if got != tc.want {
				t.Fatalf("sourceFileNameFor(%q, %q, %q, %+v, %q) = %q, want %q", tc.sourcePath, tc.releaseTitle, tc.tmdbTitle, tc.coordinates, tc.contentType, got, tc.want)
			}
		})
	}
}

func TestExtensionForContentTypeMapsWebmSeparately(t *testing.T) {
	if got := extensionForContentType("video/webm"); got != "webm" {
		t.Fatalf("video/webm = %q, want webm", got)
	}
	if got := extensionForContentType("video/x-matroska"); got != "mkv" {
		t.Fatalf("video/x-matroska = %q, want mkv", got)
	}
	if got := extensionForContentType("video/mp4; charset=binary"); got != "mp4" {
		t.Fatalf("parameterized video/mp4 = %q, want mp4", got)
	}
}
