package models

import (
	"testing"
	"time"
)

func TestMovieReleaseStatusUsesYearFallback(t *testing.T) {
	if got := MovieReleaseStatus(Title{MediaType: "movie", Year: 1999}); got != MovieReleaseStatusReleased {
		t.Fatalf("old movie status = %q, want %q", got, MovieReleaseStatusReleased)
	}
	if got := MovieReleaseStatus(Title{MediaType: "movie", Year: time.Now().Year() + 1}); got != MovieReleaseStatusUpcoming {
		t.Fatalf("future movie status = %q, want %q", got, MovieReleaseStatusUpcoming)
	}
	if got := MovieReleaseStatus(Title{MediaType: "movie", Year: time.Now().Year()}); got != MovieReleaseStatusUpcoming {
		t.Fatalf("current-year undated movie status = %q, want %q", got, MovieReleaseStatusUpcoming)
	}
}

func TestMovieReleaseStatusUsesAnyReleasedHomeWindow(t *testing.T) {
	now := time.Now()
	title := Title{
		MediaType: "movie",
		HomeRelease: &Release{
			Type: "digital",
			Date: now.AddDate(0, 1, 0).Format("2006-01-02"),
		},
		Releases: []Release{
			{Type: "digital", Date: now.AddDate(0, 1, 0).Format("2006-01-02")},
			{Type: "physical", Date: now.AddDate(0, 0, -1).Format("2006-01-02")},
		},
	}

	if got := MovieReleaseStatus(title); got != MovieReleaseStatusReleased {
		t.Fatalf("status = %q, want %q", got, MovieReleaseStatusReleased)
	}
}
