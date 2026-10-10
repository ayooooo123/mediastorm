package history

import (
	"testing"

	"novastream/models"
)

func TestEnrichSeriesWatchStateArtworkCarriesLogo(t *testing.T) {
	state := models.SeriesWatchState{SeriesID: "tmdb:movie:354912"}
	title := models.Title{Logo: &models.Image{URL: "https://image.tmdb.org/t/p/original/logo.png", Type: "logo"}}

	enrichSeriesWatchStateArtwork(&state, &title)

	if state.LogoURL != title.Logo.URL {
		t.Fatalf("expected logoUrl %q, got %q", title.Logo.URL, state.LogoURL)
	}
}
