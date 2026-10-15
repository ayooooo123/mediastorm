package metadata

import (
	"testing"

	"novastream/models"
)

func searchResult(mediaType, name string, popularity float64, votes int) models.SearchResult {
	return models.SearchResult{Title: models.Title{
		ID:         mediaType + ":" + name,
		Name:       name,
		MediaType:  mediaType,
		Popularity: popularity,
		VoteCount:  votes,
	}}
}

func rankedNames(results []models.SearchResult) []string {
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Title.MediaType + ":" + r.Title.Name
	}
	return names
}

func assertOrder(t *testing.T, got []models.SearchResult, want ...string) {
	t.Helper()
	names := rankedNames(got)
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}

func TestRankSearchResultsGildedAgeSeriesFirst(t *testing.T) {
	// Combined search used to concatenate movies, then series, then people.
	results := []models.SearchResult{
		searchResult("movie", "The Gilded Age", 1.3, 2),
		searchResult("movie", "Belle Brezing and the Gilded Age of the Bluegrass", 0.4, 0),
		searchResult("movie", "Just the Facts: Emergence of Modern America - The Gilded Age", 0.4, 0),
		searchResult("series", "The Gilded Age", 38.6, 276),
	}
	rankSearchResults("gilded age", results)
	assertOrder(t, results,
		"series:The Gilded Age",
		"movie:The Gilded Age",
		// Subtitle segment "The Gilded Age" outranks a mid-title word match.
		"movie:Just the Facts: Emergence of Modern America - The Gilded Age",
		"movie:Belle Brezing and the Gilded Age of the Bluegrass",
	)
}

func TestRankSearchResultsExactBeatsMorePopularPartialMatch(t *testing.T) {
	results := []models.SearchResult{
		searchResult("movie", "Age of Gilded Things", 60, 900),
		searchResult("series", "The Gilded Age", 38.6, 276),
	}
	rankSearchResults("Gilded Age", results)
	assertOrder(t, results, "series:The Gilded Age", "movie:Age of Gilded Things")
}

func TestRankSearchResultsBlockbusterPrefixBeatsObscureExact(t *testing.T) {
	results := []models.SearchResult{
		searchResult("movie", "Dune", 2.0, 37),
		searchResult("movie", "Dune: Part Two", 83.3, 8736),
		searchResult("movie", "Dune", 84.3, 15715),
	}
	rankSearchResults("dune", results)
	if got := results[0]; got.Title.Popularity != 84.3 {
		t.Fatalf("expected popular exact Dune first, got %v", rankedNames(results))
	}
	assertOrder(t, results, "movie:Dune", "movie:Dune: Part Two", "movie:Dune")
}

func TestRankSearchResultsUsesScoreWhenPopularityMissing(t *testing.T) {
	// Person results and TVDB-only results carry only Score.
	results := []models.SearchResult{
		{Title: models.Title{Name: "Gilded Agency", MediaType: "person"}, Score: 5},
		{Title: models.Title{Name: "The Gilded Age", MediaType: "series"}, Score: 40},
	}
	rankSearchResults("gilded age", results)
	assertOrder(t, results, "series:The Gilded Age", "person:Gilded Agency")
}

func TestRankSearchResultsStableForTies(t *testing.T) {
	results := []models.SearchResult{
		searchResult("movie", "Foo", 0, 0),
		searchResult("series", "Foo", 0, 0),
	}
	rankSearchResults("foo", results)
	assertOrder(t, results, "movie:Foo", "series:Foo")
}

func TestRankSearchResultsAlternateTitleMatches(t *testing.T) {
	results := []models.SearchResult{
		searchResult("movie", "Something Else Entirely", 5, 10),
		{Title: models.Title{Name: "Le Fabuleux Destin", MediaType: "movie", AlternateTitles: []string{"Amélie"}, Popularity: 5, VoteCount: 10}},
	}
	rankSearchResults("amelie", results)
	assertOrder(t, results, "movie:Le Fabuleux Destin", "movie:Something Else Entirely")
}

func TestRankSearchResultsSubtitleSegmentBeatsLowVoteExact(t *testing.T) {
	results := []models.SearchResult{
		searchResult("movie", "Fellowship of the Ring", 80, 4),
		searchResult("movie", "The Lord of the Rings: The Fellowship of the Ring", 78, 26000),
	}
	rankSearchResults("fellowship of the ring", results)
	assertOrder(t, results,
		"movie:The Lord of the Rings: The Fellowship of the Ring",
		"movie:Fellowship of the Ring",
	)
}

func TestRankSearchResultsPopularityDecidesBetweenPartialMatches(t *testing.T) {
	results := []models.SearchResult{
		searchResult("movie", "Daniel Brühl: Breaking Bad", 1, 3),
		searchResult("movie", "El Camino: A Breaking Bad Movie", 10, 5000),
		searchResult("series", "Breaking Bad", 300, 15000),
	}
	rankSearchResults("breaking bad", results)
	assertOrder(t, results,
		"series:Breaking Bad",
		"movie:El Camino: A Breaking Bad Movie",
		"movie:Daniel Brühl: Breaking Bad",
	)
}

func TestNormalizeSearchText(t *testing.T) {
	cases := map[string]string{
		"The Gilded Age":        "the gilded age",
		"  Amélie!  ":           "amelie",
		"Grey's Anatomy":        "greys anatomy",
		"Law & Order: SVU":      "law and order svu",
		"Spider-Man: No Way...": "spider man no way",
	}
	for in, want := range cases {
		if got := normalizeSearchText(in); got != want {
			t.Errorf("normalizeSearchText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchTitleMatchTierIgnoresLeadingArticles(t *testing.T) {
	q := normalizeSearchText("the gilded age")
	qs := stripLeadingSearchArticle(q)
	if tier := searchTitleMatchTier(q, qs, []string{"gilded", "age"}, "Gilded Age"); tier != searchMatchExact {
		t.Fatalf("tier = %v, want exact", tier)
	}
	q = normalizeSearchText("office")
	if tier := searchTitleMatchTier(q, q, []string{"office"}, "The Office PL"); tier != searchMatchPrefix {
		t.Fatalf("tier = %v, want prefix", tier)
	}
}
