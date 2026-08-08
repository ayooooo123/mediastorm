package scheduler

import (
	"testing"
	"time"

	"novastream/models"
	"novastream/services/scrob"
)

func TestScrobEventToUpdateEpisode(t *testing.T) {
	watched := true
	when := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	update := scrobEventToUpdate(scrob.HistoryEvent{Completed: true, WatchedAt: &when, Media: scrob.Media{
		TMDBID: 7124432, Type: "episode", Title: "Episode", SeasonNumber: 23, EpisodeNumber: 8,
		ShowTitle: "One Piece", ShowTMDBID: 37854, ShowTVDBID: 81797,
	}}, &watched)
	if update == nil {
		t.Fatal("expected update")
	}
	if update.ItemID != "tmdb:tv:37854:s23e08" || update.SeriesID != "tmdb:tv:37854" {
		t.Fatalf("update=%+v", update)
	}
	if update.ExternalIDs["tmdb"] != "37854" || update.ExternalIDs["episodeTmdb"] != "7124432" || update.ExternalIDs["tvdb"] != "81797" {
		t.Fatalf("ids=%v", update.ExternalIDs)
	}
}

func TestLocalItemToScrobEpisodeUsesShowAndEpisodeIDs(t *testing.T) {
	when := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	event, key, ok := localItemToScrob(models.WatchHistoryItem{
		MediaType: "episode", ItemID: "tmdb:tv:37854:s23e08", SeriesID: "tmdb:tv:37854", SeasonNumber: 23, EpisodeNumber: 8,
		Watched: true, WatchedAt: when, ExternalIDs: map[string]string{"episodeTmdb": "7124432", "tvdb": "81797"},
	})
	if !ok {
		t.Fatal("expected item to be exportable")
	}
	if key != "episode:37854:23:8" || event.SeriesTMDBID != 37854 || event.TMDBID != 7124432 || event.SeriesTVDBID != 81797 {
		t.Fatalf("key=%s event=%+v", key, event)
	}
	if event.WatchedAt == nil || !event.WatchedAt.Equal(when) {
		t.Fatalf("watchedAt=%v", event.WatchedAt)
	}
}

func TestScrobEventToUpdateSkipsIncomplete(t *testing.T) {
	watched := true
	if got := scrobEventToUpdate(scrob.HistoryEvent{Completed: false, Media: scrob.Media{Type: "movie", TMDBID: 550}}, &watched); got != nil {
		t.Fatalf("got=%+v", got)
	}
}
