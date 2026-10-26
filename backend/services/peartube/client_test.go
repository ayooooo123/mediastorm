package peartube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sync"
	"testing"
)

// fakeRelay implements the PearTube /v1 routes as src/http.js does.
type fakeRelay struct {
	mu     sync.Mutex
	jobs   []map[string]any
	source map[string]any
	auth   []string
}

var relayID = regexp.MustCompile(`^[a-z0-9]+:[a-z0-9]+(:s\d{2}e\d{2,3})?$`)
var relayJobPath = regexp.MustCompile(`^/v1/jobs/([0-9a-f-]{36})$`)

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, payload any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if header := r.Header.Get("Authorization"); header != "" {
		f.auth = append(f.auth, header)
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/v1/status":
		reply(http.StatusOK, map[string]any{"tracker": "aa", "writer": "bb", "blobs": "cc", "blobBytes": 1234, "peers": 3, "lanPeers": 2})
	case r.Method == http.MethodGet && path == "/v1/search":
		id := r.URL.Query().Get("id")
		if !relayID.MatchString(id) {
			reply(http.StatusBadRequest, map[string]string{"error": "bad id"})
			return
		}
		reply(http.StatusOK, map[string]any{"results": []map[string]any{{
			"key": "k1", "id": id, "title": "Winter.Is.Coming.1080p.mkv", "size": 42,
			"sha256": "ab", "local": true, "streamUrl": "http://relay:8175/?key=x&token=y",
		}}})
	case r.Method == http.MethodPost && path == "/v1/acquire":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			reply(http.StatusBadRequest, map[string]string{"error": "Body must be JSON"})
			return
		}
		f.source, _ = body["source"].(map[string]any)
		job := map[string]any{"jobId": "0b6f5c2e-6a7d-4d57-9a55-3c1c1f1f6a01", "id": body["id"], "title": body["title"], "status": "queued", "created": 1700000000000, "error": nil}
		f.jobs = append(f.jobs, job)
		reply(http.StatusOK, job)
	case r.Method == http.MethodGet && path == "/v1/jobs":
		reply(http.StatusOK, map[string]any{"jobs": f.jobs})
	case relayJobPath.MatchString(path) && (r.Method == http.MethodGet || r.Method == http.MethodDelete):
		jobID := relayJobPath.FindStringSubmatch(path)[1]
		for _, job := range f.jobs {
			if job["jobId"] == jobID {
				if r.Method == http.MethodDelete {
					job["status"] = "cancelled"
				}
				reply(http.StatusOK, job)
				return
			}
		}
		reply(http.StatusNotFound, map[string]string{"error": "not found"})
	default:
		reply(http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func TestClientV1Contract(t *testing.T) {
	relay := &fakeRelay{}
	server := httptest.NewServer(relay)
	defer server.Close()
	ctx := context.Background()

	// The relay has no auth for now: an empty secret sends no header.
	client, err := New(server.URL+"/", "")
	if err != nil {
		t.Fatal(err)
	}
	var apiErr *APIError
	if _, err := client.Search(ctx, "not an id"); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "bad id" {
		t.Fatalf("bad id: err = %v", err)
	}

	for _, tc := range []struct {
		imdb            string
		season, episode int
		want            string
	}{
		{"tt0111161", 0, 0, "imdb:tt0111161"},
		{"TT0944947", 1, 2, "imdb:tt0944947:s01e02"},
		{"tt0944947", 3, 104, "imdb:tt0944947:s03e104"},
		{"0944947", 1, 2, ""},
		{"tt0944947", 1, 1000, ""},
	} {
		if got := MediaID(tc.imdb, tc.season, tc.episode); got != tc.want {
			t.Errorf("MediaID(%q, %d, %d) = %q, want %q", tc.imdb, tc.season, tc.episode, got, tc.want)
		}
	}

	id := MediaID("tt0944947", 1, 2)
	results, err := client.Search(ctx, id)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	wantResult := Result{Key: "k1", ID: id, Title: "Winter.Is.Coming.1080p.mkv", Size: 42, SHA256: "ab", Local: true, StreamURL: "http://relay:8175/?key=x&token=y"}
	if len(results) != 1 || results[0] != wantResult {
		t.Fatalf("Search = %+v", results)
	}

	headers := map[string]string{"Authorization": "Bearer one-time"}
	job, err := client.Acquire(ctx, id, "Winter Is Coming", "https://cdn.example/file.mkv", headers)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if job.JobID == "" || job.ID != id || job.Title != "Winter Is Coming" || job.Status != JobQueued || job.Created != 1700000000000 || !job.Active() {
		t.Fatalf("Acquire job = %+v", job)
	}
	wantSource := map[string]any{"url": "https://cdn.example/file.mkv", "headers": map[string]any{"Authorization": "Bearer one-time"}}
	if !reflect.DeepEqual(relay.source, wantSource) {
		t.Fatalf("acquire source = %#v", relay.source)
	}

	got, err := client.Job(ctx, job.JobID)
	if err != nil || got != job {
		t.Fatalf("Job = %+v, %v", got, err)
	}
	jobs, err := client.Jobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0] != job {
		t.Fatalf("Jobs = %+v, %v", jobs, err)
	}
	cancelled, err := client.Cancel(ctx, job.JobID)
	if err != nil || cancelled.Status != JobCancelled || cancelled.Active() {
		t.Fatalf("Cancel = %+v, %v", cancelled, err)
	}
	if _, err := client.Job(ctx, "00000000-0000-0000-0000-000000000000"); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("missing job: err = %v", err)
	}

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != (Status{Tracker: "aa", Writer: "bb", Blobs: "cc", BlobBytes: 1234, Peers: 3, LanPeers: 2}) {
		t.Fatalf("Status = %+v", status)
	}
	if len(relay.auth) != 0 {
		t.Fatalf("an empty secret sent Authorization headers: %v", relay.auth)
	}
}

// The video proxy admits a private address only for a stream origin an
// authenticated relay search returned, not every port on the relay's host.
func TestSearchAdmitsOnlyReturnedStreamOrigins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"key":"k","id":"imdb:tt0111161","title":"t","size":1,"sha256":"s","local":true,"streamUrl":"http://10.9.8.7:8175/?key=x&token=y"}]}`))
	}))
	defer server.Close()
	client, err := New(server.URL, "secret-secret-secret")
	if err != nil {
		t.Fatal(err)
	}
	if IsStreamOrigin("10.9.8.7", "8175") {
		t.Fatal("origin admitted before any search returned it")
	}
	if _, err := client.Search(context.Background(), "imdb:tt0111161"); err != nil {
		t.Fatal(err)
	}
	if !IsStreamOrigin("10.9.8.7", "8175") {
		t.Fatal("returned stream origin was not admitted")
	}
	if IsStreamOrigin("10.9.8.7", "5432") {
		t.Fatal("another port on the relay host was admitted")
	}
}
