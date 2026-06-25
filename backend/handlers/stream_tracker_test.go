package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/internal/auth"
	"novastream/models"
)

func newTestTracker() *StreamTracker {
	return &StreamTracker{streams: make(map[string]*TrackedStream), stopPlaybacks: make(map[string]time.Time)}
}

func startTestStream(t *testing.T, tracker *StreamTracker, profileID, accountID string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/video/stream?profileId="+profileID, nil)
	path := "/test/file-" + string(rune('a'+tracker.Count())) + ".mkv"
	id, _, _ := tracker.StartStreamWithAccount(r, path, 1000, 0, 0, accountID)
	return id
}

func TestStartStreamMarksShareLinkScopedSessions(t *testing.T) {
	tracker := newTestTracker()

	// A request carrying a stream-scoped (share link) session is flagged.
	shareReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1", nil)
	shareReq = shareReq.WithContext(context.WithValue(shareReq.Context(),
		auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
	shareID, _, _ := tracker.StartStreamWithAccount(shareReq, "/test/shared.mkv", 1000, 0, 0, "acct1")

	// A normal full-access session request is not flagged.
	normalReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p2", nil)
	normalReq = normalReq.WithContext(context.WithValue(normalReq.Context(),
		auth.ContextKeySession, models.Session{}))
	normalID, _, _ := tracker.StartStreamWithAccount(normalReq, "/test/normal.mkv", 1000, 0, 0, "acct1")

	shared, ok := tracker.GetStream(shareID)
	if !ok || !shared.ViaShareLink {
		t.Fatalf("expected share-scoped stream to be flagged ViaShareLink, got ok=%v stream=%+v", ok, shared)
	}

	normal, ok := tracker.GetStream(normalID)
	if !ok || normal.ViaShareLink {
		t.Fatalf("expected normal stream not to be flagged ViaShareLink, got ok=%v stream=%+v", ok, normal)
	}

	for _, s := range tracker.GetActiveStreams() {
		if s.ID == shareID && !s.ViaShareLink {
			t.Errorf("GetActiveStreams dropped ViaShareLink flag for shared stream")
		}
		if s.ID == normalID && s.ViaShareLink {
			t.Errorf("GetActiveStreams set ViaShareLink flag for normal stream")
		}
	}
}

func TestGetAccountStreamUsage(t *testing.T) {
	tracker := newTestTracker()

	// No streams — usage should be 0/3
	usage := tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 0 || usage.MaxStreams != 3 || usage.AvailableStreams != 3 || usage.AtLimit {
		t.Fatalf("expected 0/3 not at limit, got %+v", usage)
	}

	// Start 2 streams for acct1
	id1 := startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p2", "acct1")

	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 2 || usage.AvailableStreams != 1 || usage.AtLimit {
		t.Fatalf("expected 2/3 not at limit, got %+v", usage)
	}

	// Start a 3rd — should be at limit
	_ = startTestStream(t, tracker, "p1", "acct1")
	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 3 || usage.AvailableStreams != 0 || !usage.AtLimit {
		t.Fatalf("expected 3/3 at limit, got %+v", usage)
	}

	// End one stream — should no longer be at limit
	tracker.EndStream(id1)
	usage = tracker.GetAccountStreamUsage("acct1", 3)
	if usage.CurrentStreams != 2 || usage.AvailableStreams != 1 || usage.AtLimit {
		t.Fatalf("after end: expected 2/3, got %+v", usage)
	}
}

func TestGetProfileStreamUsage(t *testing.T) {
	tracker := newTestTracker()

	// Start 2 streams for profile "p1"
	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p1", "acct1")
	// 1 stream for profile "p2" (same account)
	_ = startTestStream(t, tracker, "p2", "acct1")

	usage := tracker.GetProfileStreamUsage("p1", 2)
	if usage.CurrentStreams != 2 || !usage.AtLimit {
		t.Fatalf("expected 2/2 at limit for p1, got %+v", usage)
	}

	usage = tracker.GetProfileStreamUsage("p2", 2)
	if usage.CurrentStreams != 1 || usage.AtLimit {
		t.Fatalf("expected 1/2 not at limit for p2, got %+v", usage)
	}
}

func TestStreamUsageUnlimited(t *testing.T) {
	tracker := newTestTracker()

	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p1", "acct1")

	// maxStreams=0 means unlimited
	usage := tracker.GetAccountStreamUsage("acct1", 0)
	if usage.CurrentStreams != 2 || usage.AtLimit || usage.AvailableStreams != 0 {
		t.Fatalf("unlimited should never be at limit, got %+v", usage)
	}
}

func TestMarkStopPlaybackStopsMatchingHeartbeat(t *testing.T) {
	tracker := newTestTracker()
	req := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=default&profileName=godver3&mediaType=episode&itemId=tvdb:series:392276:s02e05", nil)
	id, _, _ := tracker.StartStreamWithAccount(req, "/test/file.mkv", 1000, 0, 0, "acct1")

	if !tracker.MarkStopPlayback(id) {
		t.Fatal("expected playback stop signal to be marked")
	}

	update := models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "tvdb:series:392276:s02e05"}
	if !tracker.ShouldStopPlayback("godver3", update) {
		t.Fatal("expected matching heartbeat to be told to stop")
	}

	other := models.PlaybackProgressUpdate{MediaType: "episode", ItemID: "tvdb:series:392276:s02e06"}
	if tracker.ShouldStopPlayback("godver3", other) {
		t.Fatal("did not expect different media item to be stopped")
	}
}

func TestStreamUsageCrossAccount(t *testing.T) {
	tracker := newTestTracker()

	// Streams from different accounts shouldn't interfere
	_ = startTestStream(t, tracker, "p1", "acct1")
	_ = startTestStream(t, tracker, "p2", "acct1")
	_ = startTestStream(t, tracker, "p3", "acct2")

	usage1 := tracker.GetAccountStreamUsage("acct1", 2)
	if usage1.CurrentStreams != 2 || !usage1.AtLimit {
		t.Fatalf("acct1 expected 2/2 at limit, got %+v", usage1)
	}

	usage2 := tracker.GetAccountStreamUsage("acct2", 2)
	if usage2.CurrentStreams != 1 || usage2.AtLimit {
		t.Fatalf("acct2 expected 1/2 not at limit, got %+v", usage2)
	}
}

func TestStreamUsageCollapsesRangeRequestsForSamePlayback(t *testing.T) {
	tracker := newTestTracker()

	req1 := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&profileName=User&mediaType=episode&itemId=tvdb:series:1:s01e01", nil)
	req1.Header.Set("Range", "bytes=0-4194303")
	id1, _, _ := tracker.StartStreamWithAccount(req1, "/test/file.mkv", 4194304, 0, 4194303, "acct1")
	defer tracker.EndStream(id1)

	req2 := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&profileName=User&mediaType=episode&itemId=tvdb:series:1:s01e01", nil)
	req2.Header.Set("Range", "bytes=4194304-8388607")
	id2, _, _ := tracker.StartStreamWithAccount(req2, "/test/file.mkv", 4194304, 4194304, 8388607, "acct1")
	defer tracker.EndStream(id2)

	accountUsage := tracker.GetAccountStreamUsage("acct1", 1)
	if accountUsage.CurrentStreams != 1 || !accountUsage.AtLimit {
		t.Fatalf("same playback should count as one account slot at 1/1, got %+v", accountUsage)
	}

	profileUsage := tracker.GetProfileStreamUsage("p1", 1)
	if profileUsage.CurrentStreams != 1 || !profileUsage.AtLimit {
		t.Fatalf("same playback should count as one profile slot at 1/1, got %+v", profileUsage)
	}

	_, exceeds := tracker.WouldExceedAccountLimit(req2, "/test/file.mkv", "acct1", 1)
	if exceeds {
		t.Fatal("same playback range request should not exceed account limit")
	}
}

func TestStreamUsageRejectsNewPlaybackWhenSlotLimitReached(t *testing.T) {
	tracker := newTestTracker()

	activeReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=episode&itemId=tvdb:series:1:s01e01", nil)
	activeID, _, _ := tracker.StartStreamWithAccount(activeReq, "/test/file1.mkv", 1000, 0, 999, "acct1")
	defer tracker.EndStream(activeID)

	nextReq := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=p1&mediaType=episode&itemId=tvdb:series:1:s01e02", nil)
	usage, exceeds := tracker.WouldExceedAccountLimit(nextReq, "/test/file2.mkv", "acct1", 1)
	if !exceeds {
		t.Fatal("different playback should exceed account limit")
	}
	if usage.CurrentStreams != 1 || usage.MaxStreams != 1 || !usage.AtLimit {
		t.Fatalf("expected 1/1 at limit, got %+v", usage)
	}
}

func TestMergeDashboardStreamRowsCollapsesNativeConnections(t *testing.T) {
	base := time.Date(2026, 6, 22, 22, 31, 0, 0, time.UTC)
	// Mirrors a real capture: one profile, one item (Pressure), several KSPlayer
	// byte-range connections registered as separate tracked streams.
	rows := []map[string]interface{}{
		{
			"id": "J7", "type": "direct", "profile_id": "9c234ec9", "profile_name": "Primary Profile",
			"client_ip": "127.0.0.1", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(1000), "throughput_bps": int64(500), "last_access": base,
		},
		{
			"id": "Z3", "type": "direct", "profile_id": "9c234ec9", "profile_name": "Primary Profile",
			"client_ip": "127.0.0.1", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(20), "throughput_bps": int64(0), "last_access": base.Add(24 * time.Second),
		},
		{
			"id": "A4", "type": "direct", "profile_id": "9c234ec9", "profile_name": "Primary Profile",
			"client_ip": "127.0.0.1", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(3000), "throughput_bps": int64(800), "last_access": base.Add(25 * time.Second),
		},
		// Different profile, same title -> stays a separate row.
		{
			"id": "X9", "type": "direct", "profile_id": "df1382af", "profile_name": "Amrit",
			"client_ip": "10.0.0.5", "media_type": "movie", "item_id": "tvdb:movie:358587",
			"bytes_streamed": int64(7000), "throughput_bps": int64(900), "last_access": base,
		},
	}

	merged := mergeDashboardStreamRows(rows)
	if len(merged) != 2 {
		t.Fatalf("expected 2 rows (one per profile), got %d", len(merged))
	}

	var primary map[string]interface{}
	for _, row := range merged {
		if row["profile_name"] == "Primary Profile" {
			primary = row
		}
	}
	if primary == nil {
		t.Fatal("merged Primary Profile row not found")
	}
	// Representative is the most-recently-active connection (A4).
	if primary["id"] != "A4" {
		t.Errorf("expected representative id A4 (latest activity), got %v", primary["id"])
	}
	// Bytes and throughput summed across all three connections.
	if got := primary["bytes_streamed"].(int64); got != 4020 {
		t.Errorf("expected summed bytes 4020, got %d", got)
	}
	if got := primary["throughput_bps"].(int64); got != 1300 {
		t.Errorf("expected summed throughput 1300, got %d", got)
	}
	if got := primary["connection_count"].(int); got != 3 {
		t.Errorf("expected connection_count 3, got %d", got)
	}
}
