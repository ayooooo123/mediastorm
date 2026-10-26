package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"novastream/models"
)

// appPlaybackRequest is the byte-range request an app makes: a stream path plus
// the media identity the player was launched with. This is the only playback
// signal an app produces — it never calls the progress endpoint.
func appPlaybackRequest(rangeStart int64) *http.Request {
	request := httptest.NewRequest(http.MethodGet,
		"/video/stream?profileId=profile&mediaType=movie&itemId=tmdb:movie:603"+
			"&movieName=The+Matrix&year=1999", nil)
	request.Header.Set("Range", "bytes="+strconv.FormatInt(rangeStart, 10)+"-")
	return request
}

const appStreamPath = "/debrid/torbox/55944852/file/0/The.Matrix.1999.mkv"

// A playback the tracker is already following must not re-announce itself on
// every overlapping range connection. This is the cheap half of the dedupe: it
// keeps the archiver from being called at all, whereas the claim keeps it from
// submitting twice.
func TestPlaybackStartIsAnnouncedOncePerConcurrentPlaybackSlot(t *testing.T) {
	starts := make(chan models.PlaybackProgressUpdate, 8)
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	tracker.SetPlaybackArchiver(capturePlaybackStarts{starts})

	for chunk := range 5 {
		// Overlapping connections: nothing is ended, exactly as a native player
		// keeping several ranges open at once.
		tracker.StartStreamWithAccount(
			appPlaybackRequest(int64(chunk)*4194304), appStreamPath, 4194304, int64(chunk)*4194304, 0, "acct1")
	}

	var first models.PlaybackProgressUpdate
	select {
	case first = <-starts:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the playback start")
	}
	select {
	case extra := <-starts:
		t.Fatalf("a second range connection re-announced the playback: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	// The payload has to carry both halves of what an archive needs: the media
	// coordinates the app launched with, and a stream path that can be resolved
	// again server-side.
	if first.MediaType != "movie" || first.ItemID != "tmdb:movie:603" {
		t.Fatalf("coordinates = %+v", first)
	}
	if first.MovieName != "The Matrix" || first.Year != 1999 {
		t.Fatalf("title = %q (%d)", first.MovieName, first.Year)
	}
	if first.SourcePath != appStreamPath {
		t.Fatalf("SourcePath = %q, want %q", first.SourcePath, appStreamPath)
	}
	if first.PlaybackSessionID != "direct:profile|movie:tmdb:movie:603" {
		t.Fatalf("PlaybackSessionID = %q", first.PlaybackSessionID)
	}
}

// A stream nothing identifies cannot be published, and a live channel has no
// TMDB coordinates at all. Neither may reach the archiver.
func TestPlaybackStartIsNotAnnouncedWithoutMediaIdentity(t *testing.T) {
	starts := make(chan models.PlaybackProgressUpdate, 4)
	tracker := &StreamTracker{
		streams:          map[string]*TrackedStream{},
		stopPlaybacks:    map[string]time.Time{},
		migrationSignals: map[string]playbackMigrationSignal{},
	}
	tracker.SetPlaybackArchiver(capturePlaybackStarts{starts})

	probe := httptest.NewRequest(http.MethodGet, "/video/stream?profileId=profile", nil)
	tracker.StartStreamWithAccount(probe, "/webdav/nzbs/unknown.mkv", 1000, 0, 0, "acct1")

	live := httptest.NewRequest(http.MethodGet, "/video/live?profileId=profile&mediaType=live&itemId=channel:1", nil)
	tracker.StartStreamWithAccount(live, "http://iptv.example/live.ts", 0, 0, 0, "acct1")

	select {
	case update := <-starts:
		t.Fatalf("an unpublishable playback reached the archiver: %+v", update)
	case <-time.After(100 * time.Millisecond):
	}
}

type capturePlaybackStarts struct {
	starts chan models.PlaybackProgressUpdate
}

func (c capturePlaybackStarts) OnPlaybackStarted(update models.PlaybackProgressUpdate) {
	c.starts <- update
}
