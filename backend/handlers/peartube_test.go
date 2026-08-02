package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/peartube"
	"novastream/services/streaming"
)

type fakeLibrary struct {
	item      *models.LocalMediaItem
	libraries []models.LocalMediaLibrary
}

func (f fakeLibrary) GetItem(context.Context, string) (*models.LocalMediaItem, error) {
	if f.item == nil {
		return nil, os.ErrNotExist
	}
	return f.item, nil
}

func (f fakeLibrary) ListLibraries(context.Context) ([]models.LocalMediaLibrary, error) {
	return f.libraries, nil
}

// fakeStreamResolver stands in for the composite streaming provider, which is
// what turns a /debrid/... stream path into the CDN URL it points at right now.
type fakeStreamResolver struct {
	url   string
	err   error
	asked string
}

func (f *fakeStreamResolver) GetDirectURL(_ context.Context, path string) (string, error) {
	f.asked = path
	return f.url, f.err
}

type seedCapture struct {
	fields map[string]string
	body   []byte
	// json is the decoded body of a URL seed; refusal, when set, is the error
	// envelope the relay answers a URL seed with instead of 202.
	json            map[string]any
	refusal         string
	idempotencyKeys []string
}

func newSeedRelay(t *testing.T, capture *seedCapture) *peartube.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.idempotencyKeys = append(capture.idempotencyKeys, r.Header.Get("Idempotency-Key"))
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			capture.json = map[string]any{}
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &capture.json); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if capture.refusal != "" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, capture.refusal)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"jobId":"arch_feedfacecafebeef","status":"queued","entityHint":"show:1399:s1:e2"}`)
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
				capture.body = body
				continue
			}
			capture.fields[part.FormName()] = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"jobId":"job-7","status":"queued","entityHint":"movie:603"}`)
	}))
	t.Cleanup(server.Close)
	client, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}
	return client
}

func postSeed(t *testing.T, handler *PearTubeHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/account/api/p2p/seed", bytes.NewReader(encoded))
	rec := httptest.NewRecorder()
	handler.Seed(rec, req)
	return rec
}

func TestSeedPublishesLocalMediaItem(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "The.Matrix.1999.mkv")
	if err := os.WriteFile(path, []byte("bytes-on-disk"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{
		relay: newSeedRelay(t, capture),
		localMedia: fakeLibrary{
			item: &models.LocalMediaItem{
				FilePath:       path,
				LibraryType:    models.LocalMediaLibraryTypeMovie,
				MatchedTitleID: "603",
				MatchedName:    "The Matrix",
				MatchedYear:    1999,
			},
			libraries: []models.LocalMediaLibrary{{RootPath: root}},
		},
	}

	rec := postSeed(t, handler, SeedRequest{LocalMediaItemID: "item-1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp SeedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.JobID != "job-7" || resp.Status != "queued" || resp.EntityHint != "movie:603" {
		t.Fatalf("response = %+v", resp)
	}
	if string(capture.body) != "bytes-on-disk" {
		t.Fatalf("relay received %q", capture.body)
	}
	for field, want := range map[string]string{
		"contentKind": "movie",
		"tmdbId":      "603",
		"tmdbTitle":   "The Matrix",
		"tmdbYear":    "1999",
	} {
		if capture.fields[field] != want {
			t.Fatalf("field %s = %q, want %q", field, capture.fields[field], want)
		}
	}
}

func TestSeedRejectsPathOutsideAnyLibrary(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.mkv")
	if err := os.WriteFile(outside, []byte("not yours"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{
		relay:      newSeedRelay(t, capture),
		localMedia: fakeLibrary{libraries: []models.LocalMediaLibrary{{RootPath: t.TempDir()}}},
	}

	rec := postSeed(t, handler, SeedRequest{
		FilePath:    outside,
		ContentKind: "movie",
		TMDBID:      "603",
		TMDBTitle:   "The Matrix",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.body != nil {
		t.Fatal("a file outside every library root reached the relay")
	}
}

// The whole point of the feature: seed the debrid stream the user is actually
// watching, which this backend only ever proxies.
func TestSeedPublishesAResolvedSourceURL(t *testing.T) {
	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{relay: newSeedRelay(t, capture)}

	rec := postSeed(t, handler, SeedRequest{
		SourceURL:   "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv",
		ContentKind: "episode",
		TMDBID:      "1399",
		TMDBTitle:   "Game of Thrones",
		TMDBSeason:  1,
		TMDBEpisode: 2,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp SeedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The relay's own identifiers reach the caller untouched, so a poll against
	// statusPath addresses the job the relay actually created.
	if resp.JobID != "arch_feedfacecafebeef" || resp.EntityHint != "show:1399:s1:e2" || resp.Status != "queued" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.StatusPath != "p2p/seed/arch_feedfacecafebeef" {
		t.Fatalf("statusPath = %q", resp.StatusPath)
	}
	if capture.body != nil {
		t.Fatalf("a URL seed uploaded %d bytes", len(capture.body))
	}
	if capture.json["url"] != "https://cdn.example.net/d/TOKEN/GoT.S01E02.mkv" {
		t.Fatalf("url = %#v", capture.json["url"])
	}
	if capture.json["tmdbSeason"] != float64(1) || capture.json["tmdbEpisode"] != float64(2) {
		t.Fatalf("season/episode = %#v/%#v", capture.json["tmdbSeason"], capture.json["tmdbEpisode"])
	}
}

func TestSeedOmitsEpisodeCoordinatesForAMovieURL(t *testing.T) {
	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{relay: newSeedRelay(t, capture)}

	rec := postSeed(t, handler, SeedRequest{
		SourceURL:   "https://cdn.example.net/d/TOKEN/Wedding.Crashers.mkv",
		ContentKind: "movie",
		TMDBID:      "9522",
		TMDBTitle:   "Wedding Crashers",
		TMDBYear:    2005,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"tmdbSeason", "tmdbEpisode"} {
		if _, ok := capture.json[field]; ok {
			t.Fatalf("a movie seed carried %s", field)
		}
	}
	if capture.json["tmdbYear"] != "2005" {
		t.Fatalf("tmdbYear = %#v", capture.json["tmdbYear"])
	}
}

// A URL the relay refuses is the caller's to fix, and must not look like a dead
// relay. The relay's own code has to survive the hop so the app can say why.
func TestSeedSurfacesARefusedSourceHost(t *testing.T) {
	capture := &seedCapture{
		fields:  map[string]string{},
		refusal: `{"error":{"code":"SOURCE_HOST_NOT_PUBLIC","message":"source host 10.0.0.5 is not publicly routable","field":"url"}}`,
	}
	handler := &PearTubeHandler{relay: newSeedRelay(t, capture)}

	rec := postSeed(t, handler, SeedRequest{
		SourceURL:   "https://10.0.0.5/movie.mkv",
		ContentKind: "movie",
		TMDBID:      "9522",
		TMDBTitle:   "Wedding Crashers",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "SOURCE_HOST_NOT_PUBLIC" {
		t.Fatalf("body = %v", body)
	}
	if !strings.Contains(body["error"], "not publicly routable") {
		t.Fatalf("body = %v", body)
	}
}

// A relay failure that has nothing to do with the URL must stay a relay failure.
func TestSeedReportsARelaySideFailureAsBadGateway(t *testing.T) {
	capture := &seedCapture{
		fields:  map[string]string{},
		refusal: `{"error":{"code":"UPLOAD_DIR_UNAVAILABLE","message":"the relay cannot write to its upload directory","field":null}}`,
	}
	handler := &PearTubeHandler{relay: newSeedRelay(t, capture)}

	rec := postSeed(t, handler, SeedRequest{
		SourceURL:   "https://cdn.example.net/d/TOKEN/movie.mkv",
		ContentKind: "movie",
		TMDBID:      "9522",
		TMDBTitle:   "Wedding Crashers",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// streamPath is re-resolved at seed time, because a debrid resolution is either
// short-lived or not a URL at all.
func TestSeedResolvesAStreamPathToACurrentURL(t *testing.T) {
	capture := &seedCapture{fields: map[string]string{}}
	resolver := &fakeStreamResolver{url: "https://cdn.example.net/d/FRESH-TOKEN/movie.mkv"}
	handler := &PearTubeHandler{relay: newSeedRelay(t, capture), streams: resolver}

	rec := postSeed(t, handler, SeedRequest{
		StreamPath:  "/debrid/torbox/12345/file/9/movie.mkv",
		ContentKind: "movie",
		TMDBID:      "9522",
		TMDBTitle:   "Wedding Crashers",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resolver.asked != "/debrid/torbox/12345/file/9/movie.mkv" {
		t.Fatalf("resolver was asked for %q", resolver.asked)
	}
	if capture.json["url"] != "https://cdn.example.net/d/FRESH-TOKEN/movie.mkv" {
		t.Fatalf("url = %#v", capture.json["url"])
	}
	firstKey := capture.idempotencyKeys[0]
	if firstKey == "" {
		t.Fatal("stream seed did not carry an idempotency key")
	}

	resolver.url = "https://cdn.example.net/d/ROTATED-TOKEN/movie.mkv"
	second := postSeed(t, handler, SeedRequest{
		StreamPath:  "/debrid/torbox/12345/file/9/movie.mkv",
		ContentKind: "movie",
		TMDBID:      "9522",
		TMDBTitle:   "Wedding Crashers",
	})
	if second.Code != http.StatusAccepted {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}
	if capture.idempotencyKeys[1] != firstKey {
		t.Fatalf("rotating CDN URL changed idempotency key: %q != %q", capture.idempotencyKeys[1], firstKey)
	}
}

// A debrid torrent the provider has since dropped fails to resolve. The caller
// has to re-resolve playback before it can seed, and must be told that rather
// than handed a relay error.
func TestSeedReportsAStreamPathThatNoLongerResolves(t *testing.T) {
	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{
		relay:   newSeedRelay(t, capture),
		streams: &fakeStreamResolver{err: streaming.ErrStaleTorrent},
	}

	rec := postSeed(t, handler, SeedRequest{
		StreamPath:  "/debrid/realdebrid/expired/file/1",
		ContentKind: "movie",
		TMDBID:      "9522",
		TMDBTitle:   "Wedding Crashers",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "streamPath") {
		t.Fatalf("body does not name the failing field: %s", rec.Body.String())
	}
	if capture.json != nil {
		t.Fatalf("an unresolved stream path reached the relay: %v", capture.json)
	}
}

// The streaming providers for local media and recordings resolve to filesystem
// paths. The relay cannot fetch those, and a path is not a URL to leak into a
// swarm publication either.
func TestSeedRefusesAStreamPathThatResolvesToAFile(t *testing.T) {
	capture := &seedCapture{fields: map[string]string{}}
	handler := &PearTubeHandler{
		relay:   newSeedRelay(t, capture),
		streams: &fakeStreamResolver{url: "/media/movies/Wedding Crashers.mkv"},
	}

	rec := postSeed(t, handler, SeedRequest{
		StreamPath:  "localmedia:item1/Wedding+Crashers.mkv",
		ContentKind: "movie",
		TMDBID:      "9522",
		TMDBTitle:   "Wedding Crashers",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.json != nil {
		t.Fatalf("a filesystem path reached the relay: %v", capture.json)
	}
}

func TestSeedRejectsAmbiguousAndMissingSources(t *testing.T) {
	for name, req := range map[string]SeedRequest{
		"both a local item and a url": {
			LocalMediaItemID: "item-1",
			SourceURL:        "https://cdn.example.net/d/TOKEN/movie.mkv",
			ContentKind:      "movie",
			TMDBID:           "9522",
			TMDBTitle:        "Wedding Crashers",
		},
		"no source at all":          {ContentKind: "movie", TMDBID: "9522", TMDBTitle: "Wedding Crashers"},
		"a url with no coordinates": {SourceURL: "https://cdn.example.net/d/TOKEN/movie.mkv"},
		"a stream path with no resolver": {
			StreamPath:  "/debrid/torbox/12345/file/9",
			ContentKind: "movie",
			TMDBID:      "9522",
			TMDBTitle:   "Wedding Crashers",
		},
	} {
		t.Run(name, func(t *testing.T) {
			capture := &seedCapture{fields: map[string]string{}}
			handler := &PearTubeHandler{relay: newSeedRelay(t, capture)}
			rec := postSeed(t, handler, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if capture.json != nil || capture.body != nil {
				t.Fatal("an invalid seed request reached the relay")
			}
		})
	}
}

func TestSeedIsUnavailableWithoutARelay(t *testing.T) {
	handler := &PearTubeHandler{}
	rec := postSeed(t, handler, SeedRequest{LocalMediaItemID: "item-1"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	handler.Status(statusRec, httptest.NewRequest(http.MethodGet, "/account/api/p2p/status", nil))
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Enabled {
		t.Fatal("status reported p2p enabled with no relay configured")
	}
}

func TestSeedStatusProxiesRelayJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/archive/job-7" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jobId":"job-7","status":"published","title":"The Matrix","source":{"publicationId":"pub-1","renditionId":"rend-1"}}`)
	}))
	defer server.Close()
	relay, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}

	router := mux.NewRouter()
	handler := &PearTubeHandler{relay: relay}
	router.HandleFunc("/p2p/seed/{jobId}", handler.SeedStatus).Methods(http.MethodGet)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p2p/seed/job-7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var status peartube.ArchiveStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Status != "published" || status.Source == nil || status.Source.PublicationID != "pub-1" {
		t.Fatalf("status = %+v", status)
	}
}

// The status endpoint is what an operator reads when p2p produces nothing. A
// relay that refuses to enumerate has to say so, in a form a person can act on,
// rather than looking indistinguishable from a relay that is down.
func TestStatusReportsAGatedRelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":"OPEN_ACCESS_NOT_ENABLED","message":"the relay is bound to 0.0.0.0 rather than loopback, so /api/v1/catalog and /api/v1/stream refuse to enumerate or serve media; restart the relay with --api-open (or PEARTUBE_ARCHIVE_API_OPEN=1)","field":null}}`)
	}))
	defer server.Close()
	relay, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}

	rec := httptest.NewRecorder()
	handler := &PearTubeHandler{relay: relay}
	handler.Status(rec, httptest.NewRequest(http.MethodGet, "/account/api/p2p/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Enabled          bool   `json:"enabled"`
		State            string `json:"state"`
		Reachable        bool   `json:"reachable"`
		NotOpen          bool   `json:"notOpen"`
		SeedingAvailable bool   `json:"seedingAvailable"`
		Remedy           string `json:"remedy"`
		Detail           string `json:"detail"`
		RelayURL         string `json:"relayUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Enabled || body.State != "not_open" || !body.Reachable || !body.NotOpen || !body.SeedingAvailable {
		t.Fatalf("gated status = %s", rec.Body.String())
	}
	if body.Remedy != peartube.NotOpenRemedy {
		t.Fatalf("remedy = %q, want %q", body.Remedy, peartube.NotOpenRemedy)
	}
	if body.RelayURL != relay.BaseURL() || body.Detail == "" {
		t.Fatalf("gated status = %s", rec.Body.String())
	}
}

func TestStatusReportsAReadyRelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"entities":[],"nextCursor":null}`)
	}))
	defer server.Close()
	relay, err := peartube.New(server.URL)
	if err != nil {
		t.Fatalf("peartube.New: %v", err)
	}

	rec := httptest.NewRecorder()
	handler := &PearTubeHandler{relay: relay}
	handler.Status(rec, httptest.NewRequest(http.MethodGet, "/account/api/p2p/status", nil))

	var body struct {
		State   string `json:"state"`
		NotOpen bool   `json:"notOpen"`
		Remedy  string `json:"remedy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.State != "ready" || body.NotOpen || body.Remedy != "" {
		t.Fatalf("ready status = %s", rec.Body.String())
	}
}
