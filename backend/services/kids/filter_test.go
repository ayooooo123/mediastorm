package kids

import (
	"testing"

	"novastream/models"
)

func TestFilterSearchByRatings(t *testing.T) {
	results := []models.SearchResult{
		{Score: 90, Title: models.Title{Name: "G Movie", MediaType: "movie", Certification: "G"}},
		{Score: 80, Title: models.Title{Name: "PG Movie", MediaType: "movie", Certification: "PG"}},
		{Score: 70, Title: models.Title{Name: "R Movie", MediaType: "movie", Certification: "R"}},
		{Score: 60, Title: models.Title{Name: "No Rating", MediaType: "movie"}},
		{Score: 50, Title: models.Title{Name: "TV-Y Show", MediaType: "series", Certification: "TV-Y"}},
		{Score: 40, Title: models.Title{Name: "TV-MA Show", MediaType: "series", Certification: "TV-MA"}},
	}

	filtered := FilterSearchByRatings(results, "PG", "TV-PG")

	// Expected: G Movie, PG Movie, TV-Y Show
	if len(filtered) != 3 {
		t.Fatalf("expected 3 results, got %d: %+v", len(filtered), filtered)
	}

	names := make([]string, len(filtered))
	for i, r := range filtered {
		names[i] = r.Title.Name
	}
	expected := []string{"G Movie", "PG Movie", "TV-Y Show"}
	for i, name := range expected {
		if names[i] != name {
			t.Errorf("result[%d]: expected %q, got %q", i, name, names[i])
		}
	}
}

func TestFilterSearchByRatings_EmptyRatings(t *testing.T) {
	results := []models.SearchResult{
		{Score: 90, Title: models.Title{Name: "Movie", MediaType: "movie", Certification: "R"}},
	}

	// Empty ratings = no filtering
	filtered := FilterSearchByRatings(results, "", "")
	if len(filtered) != 1 {
		t.Fatalf("expected 1 result with empty ratings, got %d", len(filtered))
	}
}

func TestFilterSearchByRatings_MovieOnlyRating(t *testing.T) {
	results := []models.SearchResult{
		{Score: 90, Title: models.Title{Name: "G Movie", MediaType: "movie", Certification: "G"}},
		{Score: 80, Title: models.Title{Name: "R Movie", MediaType: "movie", Certification: "R"}},
		{Score: 70, Title: models.Title{Name: "TV-MA Show", MediaType: "series", Certification: "TV-MA"}},
	}

	// Only movie rating set — series should pass through
	filtered := FilterSearchByRatings(results, "PG", "")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 results, got %d", len(filtered))
	}
	if filtered[0].Title.Name != "G Movie" || filtered[1].Title.Name != "TV-MA Show" {
		t.Fatalf("unexpected results: %+v", filtered)
	}
}

func TestFilterTitlesByRatings(t *testing.T) {
	titles := []models.Title{
		{Name: "G Movie", MediaType: "movie", Certification: "G"},
		{Name: "R Movie", MediaType: "movie", Certification: "R"},
		{Name: "No Rating", MediaType: "movie"},
		{Name: "TV-Y Show", MediaType: "series", Certification: "TV-Y"},
		{Name: "TV-MA Show", MediaType: "series", Certification: "TV-MA"},
	}

	filtered := FilterTitlesByRatings(titles, "PG", "TV-PG")

	// Expected: G Movie, TV-Y Show (R, unrated, and TV-MA removed)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 titles, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Name != "G Movie" || filtered[1].Name != "TV-Y Show" {
		t.Fatalf("unexpected titles: %+v", filtered)
	}
}

func TestFilterTitlesByRatings_EmptyRatings(t *testing.T) {
	titles := []models.Title{{Name: "R Movie", MediaType: "movie", Certification: "R"}}
	if got := FilterTitlesByRatings(titles, "", ""); len(got) != 1 {
		t.Fatalf("expected no filtering with empty ratings, got %d", len(got))
	}
}

func TestFilterTitlesByRatings_TVOnlyRating(t *testing.T) {
	titles := []models.Title{
		{Name: "R Movie", MediaType: "movie", Certification: "R"},
		{Name: "TV-Y Show", MediaType: "series", Certification: "TV-Y"},
		{Name: "TV-MA Show", MediaType: "series", Certification: "TV-MA"},
	}
	// Only TV rating set — movies pass through untouched.
	filtered := FilterTitlesByRatings(titles, "", "TV-PG")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 titles, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Name != "R Movie" || filtered[1].Name != "TV-Y Show" {
		t.Fatalf("unexpected titles: %+v", filtered)
	}
}

func TestValidateKidsMode_BothRejected(t *testing.T) {
	if ValidateKidsMode("both") {
		t.Fatal("expected 'both' mode to be invalid")
	}
}

func TestValidateKidsMode_ValidModes(t *testing.T) {
	for _, mode := range []string{"", "rating", "content_list"} {
		if !ValidateKidsMode(mode) {
			t.Errorf("expected mode %q to be valid", mode)
		}
	}
}

func TestFilterLibraryGroupsByRatings(t *testing.T) {
	groups := []models.LocalMediaItemGroup{
		{ID: "1", Title: "G Movie", LibraryType: models.LocalMediaLibraryTypeMovie, Certification: "G"},
		{ID: "2", Title: "R Movie", LibraryType: models.LocalMediaLibraryTypeMovie, Certification: "R"},
		{ID: "3", Title: "Unrated Movie", LibraryType: models.LocalMediaLibraryTypeMovie},
		{ID: "4", Title: "TV-Y Show", LibraryType: models.LocalMediaLibraryTypeShow, Certification: "TV-Y"},
		{ID: "5", Title: "TV-MA Show", LibraryType: models.LocalMediaLibraryTypeShow, Certification: "TV-MA"},
		{ID: "6", Title: "Plex Not Rated", LibraryType: models.LocalMediaLibraryTypeMovie, Certification: "Not Rated"},
	}

	filtered := FilterLibraryGroupsByRatings(groups, "PG", "TV-PG")

	// G Movie and TV-Y Show allowed; R, unrated and unknown labels (fail-closed), TV-MA removed.
	if len(filtered) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].ID != "1" || filtered[1].ID != "4" {
		t.Fatalf("unexpected groups: %+v", filtered)
	}
}

func TestFilterLibraryGroupsByRatings_NoCaps(t *testing.T) {
	groups := []models.LocalMediaItemGroup{
		{ID: "1", LibraryType: models.LocalMediaLibraryTypeMovie, Certification: "R"},
		{ID: "2", LibraryType: models.LocalMediaLibraryTypeMovie},
	}
	if got := FilterLibraryGroupsByRatings(groups, "", ""); len(got) != 2 {
		t.Fatalf("expected no filtering without caps, got %d", len(got))
	}
}

func TestFilterLibraryGroupsByRatings_MovieCapOnly(t *testing.T) {
	groups := []models.LocalMediaItemGroup{
		{ID: "1", LibraryType: models.LocalMediaLibraryTypeMovie, Certification: "R"},
		{ID: "2", LibraryType: models.LocalMediaLibraryTypeShow, Certification: "TV-MA"},
		{ID: "3", LibraryType: models.LocalMediaLibraryTypeOther},
	}
	filtered := FilterLibraryGroupsByRatings(groups, "PG", "")
	// Shows have no cap; movies and other files use the movie cap.
	if len(filtered) != 1 || filtered[0].ID != "2" {
		t.Fatalf("unexpected groups: %+v", filtered)
	}
}
