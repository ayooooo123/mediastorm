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

func TestNormalizeTMDBShelfSortSupportsBothDirections(t *testing.T) {
	tests := []struct {
		mediaType string
		sort      string
		want      string
	}{
		{"movie", "popularity.asc", "popularity.asc"},
		{"movie", "popularity.desc", "popularity.desc"},
		{"movie", "vote_average.asc", "vote_average.asc"},
		{"movie", "vote_average.desc", "vote_average.desc"},
		{"movie", "release_date.asc", "primary_release_date.asc"},
		{"tv", "release_date.desc", "first_air_date.desc"},
		{"movie", "title.asc", "title.asc"},
		{"tv", "title.desc", "name.desc"},
	}
	for _, test := range tests {
		if got := normalizeTMDBShelfSort(test.mediaType, test.sort); got != test.want {
			t.Errorf("normalizeTMDBShelfSort(%q, %q) = %q, want %q", test.mediaType, test.sort, got, test.want)
		}
	}
}

func TestSortTMDBShelfTitlesSupportsBothDirections(t *testing.T) {
	items := []tmdbShelfTitle{
		{Title: "Beta", Popularity: 10},
		{Title: "Alpha", Popularity: 20},
	}
	sortTMDBShelfTitles(items, "popularity.asc")
	if items[0].Title != "Beta" || items[1].Title != "Alpha" {
		t.Fatalf("popularity.asc order = %q, %q", items[0].Title, items[1].Title)
	}
	sortTMDBShelfTitles(items, "title.desc")
	if items[0].Title != "Beta" || items[1].Title != "Alpha" {
		t.Fatalf("title.desc order = %q, %q", items[0].Title, items[1].Title)
	}
}
