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
