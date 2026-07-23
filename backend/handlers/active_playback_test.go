package handlers

import (
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

func TestHLSManagerObservePlaybackActivityEnrichesMatchedSession(t *testing.T) {
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

	matched := manager.ObservePlaybackActivity("profile", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:1",
		Position:  30,
	}, 25)
	if matched != 1 {
		t.Fatalf("ObservePlaybackActivity() matched %d sessions, want 1", matched)
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
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for playback observer")
	}
}

func TestHLSManagerObservePlaybackActivityRejectsInactiveIdentity(t *testing.T) {
	observer := &capturePlaybackActivityObserver{calls: make(chan models.PlaybackProgressUpdate, 1)}
	manager := &HLSManager{
		sessions: map[string]*HLSSession{
			"session-1": {
				ID:        "session-1",
				ProfileID: "profile",
				MediaMetadata: StreamMediaMetadata{
					MediaType: "movie",
					ItemID:    "tmdb:1",
				},
			},
		},
		playbackObserver: observer,
	}

	if matched := manager.ObservePlaybackActivity("profile", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    "tmdb:2",
	}, 10); matched != 0 {
		t.Fatalf("ObservePlaybackActivity() matched %d sessions, want 0", matched)
	}
	select {
	case update := <-observer.calls:
		t.Fatalf("inactive item reached observer: %+v", update)
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
