package handlers

import (
	"fmt"
	"testing"
	"time"

	"novastream/models"
)

// TestMergeProgressIntoContinueWatching_SameSeriesID covers the common case where
// the playback progress entry and the continue-watching item share a series ID.
func TestMergeProgressIntoContinueWatching_SameSeriesID(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID:    "tvdb:series:450033",
			ExternalIDs: map[string]string{"tvdb": "450033"},
			LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
			NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			ItemID:         "tvdb:series:450033:s01e03",
			SeriesID:       "tvdb:series:450033",
			SeasonNumber:   1,
			EpisodeNumber:  3,
			PercentWatched: 66.6,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 66.6 {
		t.Fatalf("PercentWatched = %v, want 66.6", got)
	}
	if got := merged[0].ResumePercent; got != 66.6 {
		t.Fatalf("ResumePercent = %v, want 66.6", got)
	}
}

// TestMergeProgressIntoContinueWatching_MovieKeepsEnrichedProgress covers the
// desktop web Matrix regression: ListContinueWatching may include active
// playback progress, while the stored playback_progress row can still be stale
// at 0%. Startup must not erase the enriched value.
func TestMergeProgressIntoContinueWatching_MovieKeepsEnrichedProgress(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID:       "tmdb:movie:603",
			SeriesTitle:    "The Matrix",
			PercentWatched: 85.2,
			ResumePercent:  85.2,
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:      "movie",
			ItemID:         "tmdb:movie:603",
			PercentWatched: 0,
			MovieName:      "The Matrix",
			Year:           1999,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 85.2 {
		t.Fatalf("PercentWatched = %v, want 85.2", got)
	}
	if got := merged[0].ResumePercent; got != 85.2 {
		t.Fatalf("ResumePercent = %v, want 85.2", got)
	}
}

// TestMergeProgressIntoContinueWatching_SplitSeriesID reproduces the Spider-Noir
// bug: episodes recorded under one series ID (tvdb:series:450033) while the
// continue-watching item was canonicalised under a different one (tmdb:tv:220102).
// The shared external IDs must still resolve the progress bar.
func TestMergeProgressIntoContinueWatching_SplitSeriesID(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID: "tmdb:tv:220102",
			ExternalIDs: map[string]string{
				"tvdb": "450033",
				"tmdb": "220102",
				"imdb": "tt30460310",
			},
			LastWatched: models.EpisodeReference{
				SeasonNumber:  1,
				EpisodeNumber: 3,
				EpisodeID:     "tvdb:episode:11610258",
				TvdbID:        "11610258",
			},
			NextEpisode: &models.EpisodeReference{
				SeasonNumber:  1,
				EpisodeNumber: 3,
				EpisodeID:     "tvdb:episode:11610258",
				TvdbID:        "11610258",
			},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			ItemID:        "tvdb:series:450033:s01e03",
			SeriesID:      "tvdb:series:450033",
			SeasonNumber:  1,
			EpisodeNumber: 3,
			ExternalIDs: map[string]string{
				"tvdb":        "450033",
				"imdb":        "tt30460310",
				"episodeTvdb": "11610258",
			},
			PercentWatched: 66.6,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 66.6 {
		t.Fatalf("PercentWatched = %v, want 66.6 (cross-ID match failed)", got)
	}
	if got := merged[0].ResumePercent; got != 66.6 {
		t.Fatalf("ResumePercent = %v, want 66.6 (cross-ID match failed)", got)
	}
}

// TestMergeProgressIntoContinueWatching_EpisodeTvdbFallback verifies the episode
// TVDB id path matches even when no series-level external IDs overlap.
func TestMergeProgressIntoContinueWatching_EpisodeTvdbFallback(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID:    "tmdb:tv:220102",
			ExternalIDs: map[string]string{"tmdb": "220102"},
			LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3, TvdbID: "11610258"},
			NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3, TvdbID: "11610258"},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			SeriesID:       "tvdb:series:450033",
			SeasonNumber:   1,
			EpisodeNumber:  3,
			ExternalIDs:    map[string]string{"episodeTvdb": "11610258"},
			PercentWatched: 42.0,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 42.0 {
		t.Fatalf("PercentWatched = %v, want 42.0 (episode tvdb fallback failed)", got)
	}
}

// TestMergeProgressIntoContinueWatching_NoFalseMatch ensures unrelated series do
// not borrow each other's progress via the season/episode-only key space.
func TestMergeProgressIntoContinueWatching_NoFalseMatch(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID:    "tmdb:tv:999",
			ExternalIDs: map[string]string{"tmdb": "999"},
			LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
			NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			SeriesID:       "tvdb:series:450033",
			SeasonNumber:   1,
			EpisodeNumber:  3,
			ExternalIDs:    map[string]string{"tvdb": "450033"},
			PercentWatched: 66.6,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 0 {
		t.Fatalf("PercentWatched = %v, want 0 (unrelated series must not match)", got)
	}
}

// TestSelectStartupContinueWatchingItems_RetainsUpcomingPastCap verifies that a
// stale-but-upcoming series (next episode not yet aired) survives the recency cap
// so the home "My Upcoming" shelf does not silently drop it. Regression for The
// Bear dropping off after months without activity pushed it past the top-N cap.
func TestSelectStartupContinueWatchingItems_RetainsUpcomingPastCap(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)

	items := make([]models.SeriesWatchState, 0, 6)
	// 5 recent, already-aired series fill the cap (shelfLimit=3, overflow=1 => 4 slots).
	for i := 0; i < 5; i++ {
		items = append(items, models.SeriesWatchState{
			SeriesID:    fmt.Sprintf("tvdb:series:recent%d", i),
			SeriesTitle: fmt.Sprintf("Recent %d", i),
			PosterURL:   "https://image.tmdb.org/t/p/w500/recent.jpg",
			NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2},
		})
	}
	// Stale series at the end of the recency-ordered list, but with an upcoming episode.
	items = append(items, models.SeriesWatchState{
		SeriesID:    "tvdb:series:thebear",
		SeriesTitle: "The Bear",
		PosterURL:   "https://image.tmdb.org/t/p/w500/bear.jpg",
		NextEpisode: &models.EpisodeReference{
			SeasonNumber:   5,
			EpisodeNumber:  1,
			AirDate:        "2999-01-01",
			AirDateTimeUTC: future,
		},
	})

	got := selectStartupContinueWatchingItems(items, 3, 1)

	found := false
	for _, item := range got {
		if item.SeriesID == "tvdb:series:thebear" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("upcoming series dropped by cap; got %d items: %+v", len(got), seriesIDs(got))
	}
}

// TestSelectStartupContinueWatchingItems_NoDuplicateUpcoming ensures an upcoming
// series already inside the cap is not appended a second time.
func TestSelectStartupContinueWatchingItems_NoDuplicateUpcoming(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	items := []models.SeriesWatchState{
		{
			SeriesID:    "tvdb:series:onepiece",
			SeriesTitle: "One Piece",
			ExternalIDs: map[string]string{"tvdb": "81797"},
			NextEpisode: &models.EpisodeReference{AirDate: "2999-01-01", AirDateTimeUTC: future},
		},
		{
			SeriesID:    "tvdb:series:from",
			SeriesTitle: "FROM",
			NextEpisode: &models.EpisodeReference{SeasonNumber: 4, EpisodeNumber: 1},
		},
	}

	got := selectStartupContinueWatchingItems(items, 5, 2)

	count := 0
	for _, item := range got {
		if item.SeriesID == "tvdb:series:onepiece" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("One Piece appeared %d times, want 1: %+v", count, seriesIDs(got))
	}
}

func TestContinueWatchingHasUpcomingEpisode(t *testing.T) {
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		name string
		item models.SeriesWatchState
		want bool
	}{
		{"nil next episode", models.SeriesWatchState{}, false},
		{"no air date", models.SeriesWatchState{NextEpisode: &models.EpisodeReference{SeasonNumber: 1}}, false},
		{"future utc", models.SeriesWatchState{NextEpisode: &models.EpisodeReference{AirDate: "2999-01-01", AirDateTimeUTC: future}}, true},
		{"past utc", models.SeriesWatchState{NextEpisode: &models.EpisodeReference{AirDate: "2000-01-01", AirDateTimeUTC: past}}, false},
		{"future date only", models.SeriesWatchState{NextEpisode: &models.EpisodeReference{AirDate: "2999-01-01"}}, true},
		{"past date only", models.SeriesWatchState{NextEpisode: &models.EpisodeReference{AirDate: "2000-01-01"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := continueWatchingHasUpcomingEpisode(tc.item); got != tc.want {
				t.Fatalf("continueWatchingHasUpcomingEpisode = %v, want %v", got, tc.want)
			}
		})
	}
}

func seriesIDs(items []models.SeriesWatchState) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.SeriesID
	}
	return ids
}
