package handlers

import (
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/models"
)

type fakeUnreleasedVisibilityUserSettings struct {
	settings *models.UserSettings
	err      error
}

func (f fakeUnreleasedVisibilityUserSettings) Get(userID string) (*models.UserSettings, error) {
	return f.settings, f.err
}

type fakeUnreleasedVisibilityClientSettings struct {
	settings *models.ClientFilterSettings
	err      error
}

func (f fakeUnreleasedVisibilityClientSettings) Get(clientID, userID string) (*models.ClientFilterSettings, error) {
	return f.settings, f.err
}

func (f fakeUnreleasedVisibilityClientSettings) Update(clientID, userID string, settings models.ClientFilterSettings) error {
	return nil
}

func (f fakeUnreleasedVisibilityClientSettings) Delete(clientID, userID string) error {
	return nil
}

func (f fakeUnreleasedVisibilityClientSettings) DeleteByClient(clientID string) error {
	return nil
}

func (f fakeUnreleasedVisibilityClientSettings) Move(clientID, fromUserID, toUserID string) error {
	return nil
}

func TestResolveUnreleasedVisibilityPolicyPrecedence(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Display.IncludeUnreleasedMoviesInLists = false
	settings.Display.IncludeUnreleasedShowsInLists = false
	settings.Display.IncludeUnreleasedMoviesInSearch = false
	settings.Display.IncludeUnreleasedShowsInSearch = false
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	profile := &models.UserSettings{
		Display: models.DisplaySettings{
			IncludeUnreleasedMoviesInLists: models.BoolPtr(true),
		},
	}
	client := &models.ClientFilterSettings{
		IncludeUnreleasedMoviesInLists: models.BoolPtr(false),
		IncludeUnreleasedShowsInLists:  models.BoolPtr(true),
	}

	policy := resolveUnreleasedVisibilityPolicy(
		cfg,
		fakeUnreleasedVisibilityUserSettings{settings: profile},
		fakeUnreleasedVisibilityClientSettings{settings: client},
		"user-1",
		"client-1",
		unreleasedVisibilityLists,
	)

	if policy.IncludeMovies {
		t.Fatalf("expected client override to hide unreleased movies, got %+v", policy)
	}
	if !policy.IncludeShows {
		t.Fatalf("expected client override to include unreleased shows, got %+v", policy)
	}
}

func TestFilterTitlesByUnreleasedVisibility(t *testing.T) {
	titles := []models.Title{
		{Name: "Released Movie", MediaType: "movie", Theatrical: &models.Release{Date: "2025-01-01", Released: true}},
		{Name: "Upcoming Movie", MediaType: "movie", Theatrical: &models.Release{Date: "2099-01-01"}},
		{Name: "Toy Story 2", MediaType: "movie", Year: 1999},
		{Name: "Future Unknown Movie", MediaType: "movie", Year: currentYear() + 1},
		{Name: "Released Show", MediaType: "series", Status: models.SeriesReleaseStatusReleased},
		{Name: "Unreleased Show", MediaType: "series", Status: models.SeriesReleaseStatusUnreleased},
	}

	filtered := filterTitlesByUnreleasedVisibility(titles, unreleasedVisibilityPolicy{
		IncludeMovies: false,
		IncludeShows:  false,
	})

	if len(filtered) != 3 {
		t.Fatalf("expected 3 released titles, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Name != "Released Movie" || filtered[1].Name != "Toy Story 2" || filtered[2].Name != "Released Show" {
		t.Fatalf("unexpected filtered titles: %+v", filtered)
	}
}

func TestFilterWatchlistItemsByUnreleasedVisibilityUsesMovieYear(t *testing.T) {
	items := []models.WatchlistItem{
		{Name: "Toy Story 2", MediaType: "movie", Year: 1999},
		{Name: "Toy Story 5", MediaType: "movie", Year: currentYear() + 1},
	}

	filtered := filterWatchlistItemsByUnreleasedVisibility(items, unreleasedVisibilityPolicy{
		IncludeMovies: false,
		IncludeShows:  true,
	})

	if len(filtered) != 1 {
		t.Fatalf("expected old movie to remain, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Name != "Toy Story 2" {
		t.Fatalf("unexpected filtered watchlist items: %+v", filtered)
	}
}

func TestFilterSearchResultsByUnreleasedVisibilityUsesMovieStatus(t *testing.T) {
	results := []models.SearchResult{
		{Title: models.Title{Name: "Released Search Movie", MediaType: "movie", Status: models.MovieReleaseStatusReleased}},
		{Title: models.Title{Name: "Toy Story 5", MediaType: "movie", Status: models.MovieReleaseStatusUpcoming}},
		{Title: models.Title{Name: "Unknown Search Movie", MediaType: "movie", Year: currentYear() - 10, Status: models.MovieReleaseStatusUnknown}},
	}

	filtered := filterSearchResultsByUnreleasedVisibility(results, unreleasedVisibilityPolicy{
		IncludeMovies: false,
		IncludeShows:  true,
	})

	if len(filtered) != 1 {
		t.Fatalf("expected only released search movie, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Title.Name != "Released Search Movie" {
		t.Fatalf("unexpected filtered results: %+v", filtered)
	}
}

func TestFilterPersonalizedRecommendationsByUnreleasedVisibility(t *testing.T) {
	released := models.TrendingItem{Title: models.Title{Name: "Released", MediaType: "movie", Status: models.MovieReleaseStatusReleased}}
	upcoming := models.TrendingItem{Title: models.Title{Name: "Upcoming", MediaType: "movie", Status: models.MovieReleaseStatusUpcoming}}
	series := models.TrendingItem{Title: models.Title{Name: "Series", MediaType: "series", Status: models.SeriesReleaseStatusReleased}}
	response := PersonalizedRecommendationsResponse{
		Items:  []models.TrendingItem{released, upcoming, series},
		Movies: []models.TrendingItem{released, upcoming},
		Series: []models.TrendingItem{series},
		Total:  3,
	}

	filtered := filterPersonalizedRecommendationsByUnreleasedVisibility(response, unreleasedVisibilityPolicy{
		IncludeMovies: false,
		IncludeShows:  true,
	})

	if len(filtered.Items) != 2 || filtered.Total != 2 {
		t.Fatalf("filtered items/total = %d/%d, want 2/2", len(filtered.Items), filtered.Total)
	}
	if len(filtered.Movies) != 1 || filtered.Movies[0].Title.Name != "Released" {
		t.Fatalf("unexpected filtered movies: %+v", filtered.Movies)
	}
	if len(filtered.Series) != 1 {
		t.Fatalf("unexpected filtered series: %+v", filtered.Series)
	}
}
