package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/models"
)

type capturePlaybackActivityObserver struct {
	calls chan models.PlaybackProgressUpdate
}

func (c *capturePlaybackActivityObserver) HandlePlaybackUpdate(_ string, update models.PlaybackProgressUpdate, _ float64) {
	c.calls <- update
}

func TestHLSManagerKeepAliveReportsAuthoritativePlaybackState(t *testing.T) {
	observer := &capturePlaybackActivityObserver{calls: make(chan models.PlaybackProgressUpdate, 1)}
	manager := &HLSManager{
		sessions: map[string]*HLSSession{
			"session-1": {
				ID:                 "session-1",
				ProfileID:          "profile",
				Duration:           120,
				LastSegmentRequest: time.Now(),
				MediaMetadata: StreamMediaMetadata{
					MediaType: "movie",
					ItemID:    "tmdb:1",
					MovieName: "Active Movie",
					PosterURL: "https://image.example/poster.jpg",
				},
			},
		},
		playbackObserver: observer,
	}

	request := httptest.NewRequest(http.MethodPost, "/keepalive?time=30&paused=true&buffering=false", nil)
	response := httptest.NewRecorder()
	manager.KeepAlive(response, request, "session-1")
	if response.Code != http.StatusOK {
		t.Fatalf("KeepAlive() status = %d, want %d", response.Code, http.StatusOK)
	}

	select {
	case update := <-observer.calls:
		if update.PlaybackSessionID != "hls:session-1" {
			t.Fatalf("PlaybackSessionID = %q, want %q", update.PlaybackSessionID, "hls:session-1")
		}
		if update.MovieName != "Active Movie" {
			t.Fatalf("MovieName = %q, want %q", update.MovieName, "Active Movie")
		}
		if update.PosterURL != "https://image.example/poster.jpg" {
			t.Fatalf("PosterURL = %q", update.PosterURL)
		}
		if update.Duration != 120 {
			t.Fatalf("Duration = %.0f, want 120", update.Duration)
		}
		if update.Position != 30 {
			t.Fatalf("Position = %.0f, want 30", update.Position)
		}
		if !update.IsPaused || update.IsBuffering {
			t.Fatalf("playback flags = paused:%t buffering:%t", update.IsPaused, update.IsBuffering)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for playback observer")
	}

	session := manager.sessions["session-1"]
	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.PlaybackPosition != 30 || session.PlaybackUpdatedAt.IsZero() {
		t.Fatalf("live state = position:%.0f updated:%v", session.PlaybackPosition, session.PlaybackUpdatedAt)
	}
	if !session.PlaybackPaused || session.PlaybackBuffering {
		t.Fatalf("session flags = paused:%t buffering:%t", session.PlaybackPaused, session.PlaybackBuffering)
	}
}

func TestHLSManagerKeepAliveDoesNotNotifyForLiveMedia(t *testing.T) {
	observer := &capturePlaybackActivityObserver{calls: make(chan models.PlaybackProgressUpdate, 1)}
	manager := &HLSManager{
		sessions: map[string]*HLSSession{
			"session-1": {
				ID:        "session-1",
				ProfileID: "profile",
				MediaMetadata: StreamMediaMetadata{
					MediaType: "live",
					ItemID:    "channel:1",
				},
			},
		},
		playbackObserver: observer,
	}

	request := httptest.NewRequest(http.MethodPost, "/keepalive?time=10", nil)
	response := httptest.NewRecorder()
	manager.KeepAlive(response, request, "session-1")
	if response.Code != http.StatusOK {
		t.Fatalf("KeepAlive() status = %d, want %d", response.Code, http.StatusOK)
	}
	select {
	case update := <-observer.calls:
		t.Fatalf("live item reached observer: %+v", update)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestStreamTrackerObservePlaybackActivityUsesStablePlaybackIdentity(t *testing.T) {
	observer := &capturePlaybackActivityObserver{calls: make(chan models.PlaybackProgressUpdate, 2)}
	metadata := StreamMediaMetadata{
		MediaType: "movie",
		ItemID:    "tmdb:1",
		MovieName: "Active Movie",
	}
	tracker := &StreamTracker{
		streams: map[string]*TrackedStream{
			"range-1": {
				ID:            "range-1",
				ProfileID:     "profile",
				Path:          "/movie.mkv",
				LastActivity:  time.Now(),
				MediaMetadata: metadata,
			},
		},
		playbackObserver: observer,
	}
	update := models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:1",
	}

	if matched := tracker.ObservePlaybackActivity("profile", update, 10); matched != 1 {
		t.Fatalf("first ObservePlaybackActivity() matched %d streams, want 1", matched)
	}
	first := <-observer.calls

	tracker.mu.Lock()
	tracker.streams["range-2"] = &TrackedStream{
		ID:            "range-2",
		ProfileID:     "profile",
		Path:          "/movie.mkv",
		LastActivity:  time.Now().Add(time.Second),
		MediaMetadata: metadata,
	}
	tracker.mu.Unlock()

	if matched := tracker.ObservePlaybackActivity("profile", update, 20); matched != 1 {
		t.Fatalf("second ObservePlaybackActivity() matched %d streams, want 1", matched)
	}
	second := <-observer.calls

	if first.PlaybackSessionID != second.PlaybackSessionID {
		t.Fatalf("range connections produced different playback IDs: %q and %q", first.PlaybackSessionID, second.PlaybackSessionID)
	}
	if first.PlaybackSessionID != "direct:profile|movie:tmdb:1" {
		t.Fatalf("PlaybackSessionID = %q", first.PlaybackSessionID)
	}
}
