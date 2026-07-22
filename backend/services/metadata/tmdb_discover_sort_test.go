package metadata

import "testing"

func TestTMDBDiscoverSort(t *testing.T) {
	tests := []struct {
		mediaType string
		sortBy    string
		direction string
		want      string
	}{
		{"movie", "name", "asc", "title.asc"},
		{"tv", "name", "desc", "name.desc"},
		{"movie", "year", "asc", "primary_release_date.asc"},
		{"tv", "year", "desc", "first_air_date.desc"},
		{"movie", "rating", "asc", "vote_average.asc"},
		{"movie", "duration", "asc", "popularity.desc"},
	}
	for _, test := range tests {
		if got := tmdbDiscoverSort(test.mediaType, test.sortBy, test.direction); got != test.want {
			t.Errorf("tmdbDiscoverSort(%q, %q, %q) = %q, want %q", test.mediaType, test.sortBy, test.direction, got, test.want)
		}
	}
}
