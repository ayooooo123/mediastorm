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
	"testing"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/peartube"
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

type seedCapture struct {
	fields map[string]string
	body   []byte
}

func newSeedRelay(t *testing.T, capture *seedCapture) *peartube.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
