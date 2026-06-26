package apiusage

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrackerRecordAndSnapshot(t *testing.T) {
	tracker := &Tracker{endpoints: make(map[string]EndpointUsage), outbound: make(map[string]OutboundUsage)}

	tracker.Record("test.key", "Test Call", "Tests", http.MethodPost, "/admin/api/test", http.StatusOK, 12*time.Millisecond)
	tracker.Record("test.key", "Test Call", "Tests", http.MethodPost, "/admin/api/test", http.StatusInternalServerError, 8*time.Millisecond)

	entries := tracker.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Count != 2 {
		t.Fatalf("count = %d, want 2", entry.Count)
	}
	if entry.SuccessCount != 1 || entry.FailureCount != 1 {
		t.Fatalf("success/failure = %d/%d, want 1/1", entry.SuccessCount, entry.FailureCount)
	}
	if entry.TotalDurationMS != 20 {
		t.Fatalf("total duration = %d, want 20", entry.TotalDurationMS)
	}
	if entry.LastStatus != http.StatusInternalServerError {
		t.Fatalf("last status = %d, want %d", entry.LastStatus, http.StatusInternalServerError)
	}
}

func TestTrackMiddlewareRecordsImplicitOK(t *testing.T) {
	original := globalTracker
	globalTracker = &Tracker{endpoints: make(map[string]EndpointUsage)}
	t.Cleanup(func() {
		globalTracker = original
	})

	handler := Track("middleware.key", "Middleware Call", "Tests", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/test/middleware", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	entries := GetTracker().Snapshot()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].LastStatus != http.StatusOK {
		t.Fatalf("last status = %d, want %d", entries[0].LastStatus, http.StatusOK)
	}
	if entries[0].Method != http.MethodPost || entries[0].Path != "/admin/api/test/middleware" {
		t.Fatalf("method/path = %s %s", entries[0].Method, entries[0].Path)
	}
}

func TestTrackerOutboundSnapshotWindows(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	tracker := &Tracker{endpoints: make(map[string]EndpointUsage), outbound: make(map[string]OutboundUsage)}

	tracker.recordOutboundAt(now.Add(-25*time.Hour), "Torrentio", "Stream search", http.MethodGet, "https://torrentio.strem.fun/stream/movie/tt1111111.json", http.StatusOK, 30*time.Millisecond, false)
	tracker.recordOutboundAt(now.Add(-2*time.Hour), "Torrentio", "Stream search", http.MethodGet, "https://torrentio.strem.fun/stream/movie/tt0903747.json", http.StatusTooManyRequests, 20*time.Millisecond, false)
	tracker.recordOutboundAt(now.Add(-30*time.Minute), "Torrentio", "Stream search", http.MethodGet, "https://torrentio.strem.fun/stream/movie/tt0133093.json?apikey=secret", http.StatusOK, 10*time.Millisecond, false)

	entries := tracker.snapshotOutboundAt(now)
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbound entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Count != 3 {
		t.Fatalf("count = %d, want 3", entry.Count)
	}
	if entry.LastHourCount != 1 {
		t.Fatalf("last hour count = %d, want 1", entry.LastHourCount)
	}
	if entry.Last24HourCount != 2 {
		t.Fatalf("last 24h count = %d, want 2", entry.Last24HourCount)
	}
	if entry.FailureCount != 1 {
		t.Fatalf("failure count = %d, want 1", entry.FailureCount)
	}
	if entry.LastPath != "/stream/movie/tt0133093.json" {
		t.Fatalf("last path = %q, want path without query string", entry.LastPath)
	}
}

func TestTrackerStoragePersistsAndLoadsOutboundEvents(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	tracker := &Tracker{endpoints: make(map[string]EndpointUsage), outbound: make(map[string]OutboundUsage)}
	tracker.storageDir = dir

	tracker.recordOutboundAt(now, "Torrentio", "Stream search", http.MethodGet, "https://torrentio.strem.fun/stream/movie/tt0133093.json?apikey=secret", http.StatusOK, 10*time.Millisecond, true)

	loaded := &Tracker{endpoints: make(map[string]EndpointUsage), outbound: make(map[string]OutboundUsage), storageDir: dir}
	if err := loaded.loadOutboundEvents(now.Add(5 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	entries := loaded.snapshotOutboundAt(now.Add(5 * time.Minute))
	if len(entries) != 1 {
		t.Fatalf("expected 1 loaded entry, got %d", len(entries))
	}
	if entries[0].Last24HourCount != 1 || entries[0].LastHourCount != 1 {
		t.Fatalf("loaded counts = hour %d day %d, want 1/1", entries[0].LastHourCount, entries[0].Last24HourCount)
	}
	if entries[0].LastPath != "/stream/movie/tt0133093.json" {
		t.Fatalf("last path = %q, want sanitized path", entries[0].LastPath)
	}
}

func TestDoRecordsOutboundRequest(t *testing.T) {
	original := globalTracker
	globalTracker = &Tracker{endpoints: make(map[string]EndpointUsage), outbound: make(map[string]OutboundUsage)}
	t.Cleanup(func() {
		globalTracker = original
	})

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}

	req, err := http.NewRequest(http.MethodPost, "https://torrentio.strem.fun/stream/movie/tt0133093.json?token=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Do(client, "Torrentio", "Stream search", req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	entries := GetTracker().SnapshotOutbound()
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbound entry, got %d", len(entries))
	}
	if entries[0].Provider != "Torrentio" || entries[0].Operation != "Stream search" {
		t.Fatalf("provider/operation = %q/%q", entries[0].Provider, entries[0].Operation)
	}
	if entries[0].LastStatus != http.StatusCreated {
		t.Fatalf("last status = %d, want %d", entries[0].LastStatus, http.StatusCreated)
	}
	if entries[0].LastPath != "/stream/movie/tt0133093.json" {
		t.Fatalf("last path = %q, want path without query string", entries[0].LastPath)
	}
}

func TestTrackerRedactsSensitivePathSegments(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	tracker := &Tracker{endpoints: make(map[string]EndpointUsage), outbound: make(map[string]OutboundUsage)}

	tracker.recordOutboundAt(now, "Comet", "Stream search", http.MethodGet, "https://comet.example/config-apiKey-secret-token-value/stream/movie/tt0133093.json", http.StatusOK, 10*time.Millisecond, false)

	entries := tracker.snapshotOutboundAt(now)
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbound entry, got %d", len(entries))
	}
	if entries[0].LastPath != "/[redacted]/stream/movie/tt0133093.json" {
		t.Fatalf("last path = %q, want redacted path", entries[0].LastPath)
	}
}

func TestTrackClientRecordsOutboundRequest(t *testing.T) {
	original := globalTracker
	globalTracker = &Tracker{endpoints: make(map[string]EndpointUsage), outbound: make(map[string]OutboundUsage)}
	t.Cleanup(func() {
		globalTracker = original
	})

	base := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}
	client := TrackClient(base, "TMDB", "Metadata API")
	req, err := http.NewRequest(http.MethodGet, "https://api.themoviedb.org/3/movie/550?api_key=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	entries := GetTracker().SnapshotOutbound()
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbound entry, got %d", len(entries))
	}
	if entries[0].Provider != "TMDB" || entries[0].Operation != "Metadata API" {
		t.Fatalf("provider/operation = %q/%q", entries[0].Provider, entries[0].Operation)
	}
	if entries[0].LastStatus != http.StatusAccepted {
		t.Fatalf("last status = %d, want %d", entries[0].LastStatus, http.StatusAccepted)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
