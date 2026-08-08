package scrob

import (
	"testing"
	"time"
)

func TestScrobEpisodeEventUsesShowCoordinates(t *testing.T) {
	when := time.Date(2026, 8, 8, 12, 0, 0, 0, time.FixedZone("test", -6*60*60))
	event, ok := scrobEpisodeEvent(81797, 23, 8, when, map[string]string{"tmdb": "37854", "episodeTmdb": "7124432"})
	if !ok {
		t.Fatal("expected event")
	}
	if event.SeriesTMDBID != 37854 || event.SeriesTVDBID != 81797 || event.TMDBID != 7124432 || event.SeasonNumber != 23 || event.EpisodeNumber != 8 {
		t.Fatalf("event=%+v", event)
	}
	if event.WatchedAt == nil || !event.WatchedAt.Equal(when.UTC()) {
		t.Fatalf("watchedAt=%v", event.WatchedAt)
	}
}

func TestScrobEpisodeEventRequiresShowTMDBID(t *testing.T) {
	if _, ok := scrobEpisodeEvent(81797, 1, 2, time.Now(), map[string]string{"tvdb": "81797"}); ok {
		t.Fatal("expected TVDB-only episode to be skipped because Scrob requires series_tmdb_id")
	}
}
