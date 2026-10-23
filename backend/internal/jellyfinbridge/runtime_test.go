package jellyfinbridge

import (
	"net/http"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// Plezy treats an end more than 10 s before the item's runtime as a dropped
// stream and reloads the episode instead of moving on. Items therefore
// report the measured length of their stream once it is known, and the next
// episode is measured while the current one plays.
func TestItemsReportMeasuredRuntime(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.PrequeueStatus = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{
			PrequeueID: "pq1", Status: playback.PrequeueStatusReady, StreamPath: "/webdav/episode.mkv",
			SelectedAudioTrack: -1, SelectedSubtitleTrack: -1,
		})
	}
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"durationSeconds": 1452.5}) // 24:12.5, metadata says 25 min or so
	}
	tok := h.login("Living Room", "alice-pass")
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	seriesID := next.Items[0].SeriesID
	seasons := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Seasons", tok, nil))
	eps := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Episodes?SeasonId="+seasons.Items[0].ID, tok, nil))
	first, second := eps.Items[0].ID, eps.Items[1].ID
	measured := secondsToTicks(1452.5)
	runtime := func(id string) int64 {
		item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+id, tok, nil))
		if item.RunTimeTicks == nil {
			return 0
		}
		return *item.RunTimeTicks
	}
	if runtime(first) == measured {
		t.Fatal("runtime measured before anything was played")
	}

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+first+"/PlaybackInfo", tok, map[string]any{}))
	if got := runtime(first); got != measured {
		t.Fatalf("runtime after PlaybackInfo = %d, want the measured %d", got, measured)
	}
	// The episode list (Plezy's queue) carries it too.
	all := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Episodes", tok, nil))
	if all.Items[0].RunTimeTicks == nil || *all.Items[0].RunTimeTicks != measured {
		t.Fatalf("episode list runtime = %v", all.Items[0].RunTimeTicks)
	}

	// Starting the first episode measures the next one.
	h.do(http.MethodPost, "/Sessions/Playing", tok, map[string]any{"ItemId": first, "PlaySessionId": info.PlaySessionID, "PositionTicks": 0})
	deadline := time.Now().Add(5 * time.Second)
	for runtime(second) != measured {
		if time.Now().After(deadline) {
			t.Fatalf("next episode runtime = %d, want the measured %d", runtime(second), measured)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
