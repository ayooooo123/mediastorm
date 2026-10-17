package jellyfinbridge

import (
	"net/http"
	"strings"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

// A season shows how many aired episodes are unplayed, and a check mark once
// all are played; marking one unplayed takes the check mark away again.
func TestSeasonPlayedState(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	seriesID := next.Items[0].SeriesID
	season := func() dto.UserItemData {
		seasons := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Seasons", tok, nil))
		if seasons.TotalRecordCount != 1 || seasons.Items[0].UserData == nil {
			t.Fatalf("seasons = %+v", seasons)
		}
		return *seasons.Items[0].UserData
	}
	if d := season(); d.Played || d.UnplayedItemCount == nil || *d.UnplayedItemCount != 2 {
		t.Fatalf("fresh season = played %v unplayed %v, want 2 unplayed", d.Played, d.UnplayedItemCount)
	}
	seasons := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Seasons", tok, nil))
	eps := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Episodes?SeasonId="+seasons.Items[0].ID, tok, nil))
	h.do(http.MethodPost, "/UserPlayedItems/"+eps.Items[0].ID, tok, nil)
	if d := season(); d.Played || d.UnplayedItemCount == nil || *d.UnplayedItemCount != 1 {
		t.Fatalf("one played: played %v unplayed %v", d.Played, d.UnplayedItemCount)
	}
	h.do(http.MethodPost, "/UserPlayedItems/"+eps.Items[1].ID, tok, nil)
	if d := season(); !d.Played || *d.UnplayedItemCount != 0 {
		t.Fatalf("all played: played %v unplayed %v", d.Played, *d.UnplayedItemCount)
	}
	h.do(http.MethodDelete, "/UserPlayedItems/"+eps.Items[1].ID, tok, nil)
	if d := season(); d.Played {
		t.Fatal("season still played after marking an episode unplayed")
	}
}

// A list request that read the history before a "mark played" must not put
// its stale state into the cache.
func TestUserStateCacheSkipsStaleLoad(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	id := h.alice.ID
	h.history.onList = func() { h.srv.invalidateUserState(id) } // the write lands mid-load
	h.srv.newUserState(id).load()
	h.srv.states.mu.Lock()
	_, cached := h.srv.states.entries[id]
	h.srv.states.mu.Unlock()
	if cached {
		t.Fatal("state read before a write was cached")
	}
	h.history.onList = nil
	h.srv.newUserState(id).load()
	h.srv.states.mu.Lock()
	_, cached = h.srv.states.entries[id]
	h.srv.states.mu.Unlock()
	if !cached {
		t.Fatal("a clean load was not cached")
	}
}

// Episode-scoped IDs from a history row must not reach other episodes: with
// them mediastorm took S01E02 for S01E01 and stored its flag in that row.
func TestMarkPlayedSendsOnlySeriesIDs(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	seriesID := next.Items[0].SeriesID
	// A history row's IDs reach the series context (as the series count did).
	ref, _ := h.srv.ids.get(seriesID)
	polluted := titleFromRef(ref)
	polluted.ExternalIDs = map[string]string{"tmdb": "1399", "episodeTmdb": "63056", "episodeTvdb": "3254641", "absoluteEpisode": "1"}
	h.srv.ids.put(refSeries, ref.Source, polluted.context())

	seasons := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Seasons", tok, nil))
	eps := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/"+seriesID+"/Episodes?SeasonId="+seasons.Items[0].ID, tok, nil))
	h.do(http.MethodPost, "/UserPlayedItems/"+eps.Items[1].ID, tok, nil)
	rows, _ := h.history.ListWatchHistory(h.alice.ID)
	if len(rows) == 0 {
		t.Fatal("nothing stored")
	}
	last := rows[len(rows)-1]
	for key := range last.ExternalIDs {
		if strings.HasPrefix(strings.ToLower(key), "episode") || strings.HasPrefix(strings.ToLower(key), "absolute") {
			t.Fatalf("episode-scoped id %q sent with %s: %v", key, last.ItemID, last.ExternalIDs)
		}
	}
	if last.EpisodeNumber != 2 {
		t.Fatalf("stored episode %d, want 2", last.EpisodeNumber)
	}
}
