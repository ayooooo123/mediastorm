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
