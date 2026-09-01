package scrob

import (
	"testing"

	"novastream/models"
)

func TestBuildManualSessionStartMovie(t *testing.T) {
	request, ok := buildManualSessionStart(models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:movie:550", MovieName: "Fight Club", Duration: 8340,
	})
	if !ok || request.TMDBID != 550 || request.Runtime != 139 || request.Title != "Fight Club" {
		t.Fatalf("request=%+v ok=%v", request, ok)
	}
}

func TestBuildManualSessionStartEpisodePreservesSpecialSeason(t *testing.T) {
	request, ok := buildManualSessionStart(models.PlaybackProgressUpdate{
		MediaType: "episode", ItemID: "episode", SeriesID: "tmdb:tv:42",
		SeasonNumber: 0, EpisodeNumber: 3, EpisodeName: "Special", Duration: 1439,
		ExternalIDs: map[string]string{"episodeTmdb": "99"},
	})
	if !ok || request.TMDBID != 99 || request.ShowTMDBID != 42 || request.Runtime != 24 {
		t.Fatalf("request=%+v ok=%v", request, ok)
	}
	if request.SeasonNumber == nil || *request.SeasonNumber != 0 || request.EpisodeNumber == nil || *request.EpisodeNumber != 3 {
		t.Fatalf("episode coordinates were not preserved: %+v", request)
	}
}

func TestBuildManualSessionStartEpisodeRequiresEpisodeTMDBID(t *testing.T) {
	request, ok := buildManualSessionStart(models.PlaybackProgressUpdate{
		MediaType: "episode", ItemID: "tmdb:tv:95557:s02e08", SeriesID: "tmdb:tv:95557",
		SeasonNumber: 2, EpisodeNumber: 8, EpisodeName: "The Colossaeus (2)", Duration: 1260,
		ExternalIDs: map[string]string{"tmdb": "95557"},
	})
	if ok {
		t.Fatalf("request=%+v ok=%v, want unidentified episode session rejected", request, ok)
	}
}
