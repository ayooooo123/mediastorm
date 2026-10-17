package jellyfinbridge

import (
	"slices"
	"testing"
)

// mediastorm stored the episode under the series' TMDB ID; the bridge knew
// the series only by its TVDB ID and keyed the episode that way.
func TestHistoryKeysMatchEitherSeriesID(t *testing.T) {
	ext := map[string]string{"imdb": "tt0500092", "tmdb": "1087", "tvdb": "78814", "titleId": "tvdb:series:78814"}
	keys := historyKeys("episode", "tmdb:tv:1087:s01e01", ext, 1, 1)
	for _, want := range []string{"episode:tmdb:tv:1087:s01e01", "episode:tvdb:series:78814:s01e01"} {
		if !slices.Contains(keys, want) {
			t.Errorf("keys %v lack %q", keys, want)
		}
	}
	if got := historyKeys("movie", "tmdb:movie:603", map[string]string{"tmdb": "603"}, 0, 0); len(got) != 1 {
		t.Errorf("movie keys = %v", got)
	}
}
