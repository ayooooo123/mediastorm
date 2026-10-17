package jellyfinbridge

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

type fakeCalendar struct{ items []models.CalendarItem }

func (f fakeCalendar) GetForHomeShelf(string, *time.Location, int, int, int) []models.CalendarItem {
	return f.items
}

func TestSimilarItems(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	res := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items/"+item+"/Similar?Limit=1", tok, nil))
	if res.TotalRecordCount != 1 || res.Items[0].Name != "The Matrix Reloaded" || res.Items[0].Type != dto.KindMovie {
		t.Fatalf("similar = %+v", res)
	}
	// The similar title opens like any other item.
	if rec := h.do(http.MethodGet, "/Items/"+res.Items[0].ID, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("similar item lookup = %d", rec.Code)
	}
	if res := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items/0123456789abcdef0123456789abcdef/Similar", tok, nil)); res.TotalRecordCount != 0 {
		t.Fatalf("unknown item similar = %+v", res)
	}
}

func TestUpcomingEpisodes(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tomorrow := time.Now().Add(24 * time.Hour).Format("2006-01-02")
	nextWeek := time.Now().Add(7 * 24 * time.Hour).Format("2006-01-02")
	h.srv.deps.Calendar = fakeCalendar{items: []models.CalendarItem{
		{Title: "Severance", MediaType: "series", SeasonNumber: 2, EpisodeNumber: 4, EpisodeTitle: "Later", AirDate: nextWeek, Source: "watchlist", ExternalIDs: map[string]string{"tmdb": "95396"}},
		{Title: "Severance", MediaType: "series", SeasonNumber: 2, EpisodeNumber: 3, EpisodeTitle: "Sooner", AirDate: tomorrow, AirTime: "21:00", Source: "watchlist", ExternalIDs: map[string]string{"tmdb": "95396"}},
		{Title: "Somebody Else's Show", MediaType: "series", SeasonNumber: 1, EpisodeNumber: 1, AirDate: tomorrow, Source: "trending", ExternalIDs: map[string]string{"tmdb": "1"}},
		{Title: "A Movie", MediaType: "movie", AirDate: tomorrow, Source: "watchlist", ExternalIDs: map[string]string{"tmdb": "2"}},
	}}
	tok := h.login("Living Room", "alice-pass")
	res := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/Upcoming?Limit=10", tok, nil))
	if res.TotalRecordCount != 2 || res.Items[0].Name != "Sooner" || res.Items[1].Name != "Later" {
		t.Fatalf("upcoming = %+v", res.Items)
	}
	ep := res.Items[0]
	if ep.Type != dto.KindEpisode || ep.SeriesName != "Severance" || ep.PremiereDate == nil || *ep.ParentIndexNumber != 2 || *ep.IndexNumber != 3 {
		t.Fatalf("upcoming episode = %+v", ep)
	}
}

func TestMediaSegmentsForEpisodes(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	var body map[string]any
	calls := 0
	h.srv.deps.Handlers.SkipSegments = func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, http.StatusOK, map[string]any{"segments": []map[string]any{
			{"type": "intro", "start": 60.0, "end": 90.5, "source": "introdb"},
			{"type": "outro", "start": 1300.0, "end": 1380.0, "source": "skipdb"},
			{"type": "bogus", "start": 1.0, "end": 2.0},
		}})
	}
	tok := h.login("Living Room", "alice-pass")
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	episode := next.Items[0].ID // Game of Thrones S01E02

	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+episode+"/PlaybackInfo", tok, map[string]any{}))
	src := info.MediaSources[0]
	if !src.HasSegments {
		t.Fatal("episode media source does not advertise segments")
	}
	// jellyfin-web asks by MediaSourceId, Android TV by item ID.
	for _, id := range []string{src.ID, episode} {
		res := decode[dto.QueryResult[dto.MediaSegmentDto]](t, h.do(http.MethodGet, "/MediaSegments/"+id, tok, nil))
		if res.TotalRecordCount != 2 || res.Items[0].Type != "Intro" || res.Items[0].StartTicks != 600000000 || res.Items[0].EndTicks != 905000000 || res.Items[1].Type != "Outro" {
			t.Fatalf("segments by %s = %+v", id, res.Items)
		}
	}
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 (cached per play session)", calls)
	}
	if body["imdbId"] != "tt0944947" || body["season"] != float64(1) || body["episode"] != float64(2) {
		t.Fatalf("resolver request = %v", body)
	}
	res := decode[dto.QueryResult[dto.MediaSegmentDto]](t, h.do(http.MethodGet, "/MediaSegments/"+src.ID+"?includeSegmentTypes=Outro", tok, nil))
	if res.TotalRecordCount != 1 || res.Items[0].Type != "Outro" {
		t.Fatalf("filtered segments = %+v", res.Items)
	}
	// Another profile cannot read this play session's segments.
	other := h.login("bob/Kids", "bob-pass")
	if res := decode[dto.QueryResult[dto.MediaSegmentDto]](t, h.do(http.MethodGet, "/MediaSegments/"+src.ID, other, nil)); res.TotalRecordCount != 0 {
		t.Fatalf("other profile got segments: %+v", res.Items)
	}
}
