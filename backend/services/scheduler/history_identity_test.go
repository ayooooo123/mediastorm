package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/simkl"
)

func onePieceIdentityDetails() *models.SeriesDetails {
	return &models.SeriesDetails{
		Title: models.Title{TMDBID: 37854, TVDBID: 81797, IMDBID: "tt0388629"},
		Seasons: []models.SeriesSeason{{Number: 23, Episodes: []models.SeriesEpisode{
			{TMDBID: 100018, TVDBID: 200018, Name: "Different Episode", SeasonNumber: 23, EpisodeNumber: 18, AbsoluteEpisodeNumber: 1174},
			{TMDBID: 7550164, TVDBID: 11899999, Name: "A Nightmarish Game", SeasonNumber: 23, EpisodeNumber: 17, AbsoluteEpisodeNumber: 1173},
		}}},
	}
}

func TestMatchHistoryEpisodePrioritizesEpisodeIDOverExactCoordinates(t *testing.T) {
	match := matchHistoryEpisode(onePieceIdentityDetails(), models.WatchHistoryItem{
		MediaType: "episode", Name: "A Nightmarish Game", SeasonNumber: 23, EpisodeNumber: 18,
		ExternalIDs: map[string]string{"episodeTmdb": "7550164"},
	})
	if match == nil || match.TMDBID != 7550164 || match.EpisodeNumber != 17 {
		t.Fatalf("match=%+v", match)
	}
}

func TestCanonicalizeProviderEpisodePrioritizesEpisodeIDOverCoordinates(t *testing.T) {
	metadata := &fakeSchedulerMetadataService{details: onePieceIdentityDetails()}
	svc := &Service{metadataService: metadata}
	episodeIDs := map[string]string{"tmdb": "7550164"}
	season, episode, absolute, title := svc.canonicalizeProviderEpisode(
		"test", map[string]string{"tmdb": "37854", "tvdb": "81797"}, episodeIDs, 23, 18, 0, "Provider title",
	)
	if season != 23 || episode != 17 || absolute != 1173 || title != "A Nightmarish Game" {
		t.Fatalf("canonical=%d:%d absolute=%d title=%q", season, episode, absolute, title)
	}
	if metadata.lastQuery.TVDBID != 81797 || metadata.lastQuery.TMDBID != 37854 {
		t.Fatalf("canonicalization discarded provider IDs: %+v", metadata.lastQuery)
	}
}

func TestEnrichAndCollapseHistoryItemsCollapsesNumberingAliasesToNewestState(t *testing.T) {
	older := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	items := []models.WatchHistoryItem{
		{
			MediaType: "episode", ItemID: "tvdb:series:81797:s23e17", SeriesID: "tvdb:series:81797", Name: "A Nightmarish Game",
			SeasonNumber: 23, EpisodeNumber: 17, Watched: true, WatchedAt: older, UpdatedAt: older,
			ExternalIDs: map[string]string{"tmdb": "37854", "tvdb": "81797", "episodeTvdb": "11899999"},
		},
		{
			MediaType: "episode", ItemID: "tmdb:tv:37854:s23e1173", SeriesID: "tmdb:tv:37854", Name: "A Nightmarish Game",
			SeasonNumber: 23, EpisodeNumber: 1173, Watched: false, UpdatedAt: newer,
			ExternalIDs: map[string]string{"tmdb": "37854", "tvdb": "81797", "absoluteEpisode": "1173"},
		},
	}
	svc := &Service{metadataService: &fakeSchedulerMetadataService{details: onePieceIdentityDetails()}}
	got := svc.enrichAndCollapseHistoryItems(items)
	if len(got) != 1 {
		t.Fatalf("items=%+v", got)
	}
	if got[0].Watched || got[0].SeasonNumber != 23 || got[0].EpisodeNumber != 17 {
		t.Fatalf("collapsed=%+v", got[0])
	}
	if got[0].ExternalIDs["episodeTmdb"] != "7550164" || got[0].ExternalIDs["absoluteEpisode"] != "1173" {
		t.Fatalf("ids=%v", got[0].ExternalIDs)
	}
}

func TestBuildMDBListRemovalPayloadUsesCanonicalEpisodeCoordinates(t *testing.T) {
	movies, shows, skipped := buildMDBListRemovalPayload([]models.WatchHistoryItem{{
		MediaType: "episode", ItemID: "tmdb:tv:37854:s23e17", SeriesID: "tmdb:tv:37854",
		SeasonNumber: 23, EpisodeNumber: 17, ExternalIDs: map[string]string{"tmdb": "37854", "imdb": "tt0388629"},
	}})
	if len(movies) != 0 || len(shows) != 1 || skipped != 0 {
		t.Fatalf("movies=%v shows=%v skipped=%d", movies, shows, skipped)
	}
	seasons := shows[0]["seasons"].([]map[string]interface{})
	episodes := seasons[0]["episodes"].([]map[string]interface{})
	if seasons[0]["number"] != 23 || episodes[0]["number"] != 17 {
		t.Fatalf("shows=%v", shows)
	}
}

func TestSimklExportEpisodeCoordinatesUsesAbsoluteAnimeOrder(t *testing.T) {
	season, episode := simklExportEpisodeCoordinates(models.WatchHistoryItem{
		SeasonNumber: 23, EpisodeNumber: 17, ExternalIDs: map[string]string{"absoluteEpisode": "1173"},
	})
	if season != 1 || episode != 1173 {
		t.Fatalf("coordinates=%d:%d", season, episode)
	}
	season, episode = simklExportEpisodeCoordinates(models.WatchHistoryItem{
		SeasonNumber: 2, EpisodeNumber: 1, ExternalIDs: map[string]string{"absoluteEpisode": "11"},
	})
	if season != 2 || episode != 1 {
		t.Fatalf("ordinary coordinates=%d:%d", season, episode)
	}
}

func TestSyncLocalHistoryToSimklPropagatesCanonicalEpisodeUnwatch(t *testing.T) {
	historySvc, err := history.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	watched, unwatched := true, false
	when := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC)
	update := models.WatchHistoryUpdate{
		MediaType: "episode", ItemID: "tmdb:tv:37854:s23e1173", SeriesID: "tmdb:tv:37854", Name: "A Nightmarish Game",
		SeasonNumber: 23, EpisodeNumber: 1173, Watched: &watched, WatchedAt: when,
		ExternalIDs: map[string]string{"tmdb": "37854", "tvdb": "81797", "simkl": "38636", "absoluteEpisode": "1173"},
	}
	if _, err := historySvc.UpdateWatchHistory("profile", update); err != nil {
		t.Fatal(err)
	}
	update.Watched, update.WatchedAt = &unwatched, when.Add(time.Minute)
	if _, err := historySvc.UpdateWatchHistory("profile", update); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/history/remove" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		var req simkl.SyncHistoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.Shows) != 1 || len(req.Shows[0].Seasons) != 1 || len(req.Shows[0].Seasons[0].Episodes) != 1 {
			t.Fatalf("request=%+v", req)
		}
		if req.Shows[0].Seasons[0].Number != 1 || req.Shows[0].Seasons[0].Episodes[0].Number != 1173 {
			t.Fatalf("request=%+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	simkl.SetBaseURLForTest(server.URL)
	defer simkl.SetBaseURLForTest("https://api.simkl.com")

	svc := &Service{
		historyService: historySvc, simklClient: simkl.NewClient(),
		metadataService:   &fakeSchedulerMetadataService{details: onePieceIdentityDetails()},
		lastFullSyncTimes: make(map[string]time.Time),
	}
	lastRun := when.Add(-time.Minute)
	result, err := svc.syncLocalHistoryToSimkl(config.ScheduledTask{ID: "simkl-test", LastRunAt: &lastRun}, &config.SimklAccount{ClientID: "client", AccessToken: "token"}, "profile", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Fatalf("count=%d", result.Count)
	}
}
