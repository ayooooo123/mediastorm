package metadata

import (
	"testing"

	"novastream/models"
)

func season(num int, typeKey, typeName string) tvdbSeason {
	return tvdbSeason{Number: num, Type: tvdbSeasonType{Type: typeKey, Name: typeName}}
}

func TestAvailableSeriesOrderings(t *testing.T) {
	t.Run("single ordering returns nil", func(t *testing.T) {
		seasons := []tvdbSeason{
			season(1, "official", "Aired Order"),
			season(2, "official", "Aired Order"),
		}
		if got := availableSeriesOrderings(seasons); got != nil {
			t.Fatalf("expected nil for single ordering, got %+v", got)
		}
	})

	t.Run("multiple orderings counted per type", func(t *testing.T) {
		// Money Heist-style: official re-split vs the original aired ordering.
		seasons := []tvdbSeason{
			season(0, "official", "Aired Order"), // specials, not counted
			season(1, "official", "Aired Order"),
			season(2, "official", "Aired Order"),
			season(3, "official", "Aired Order"),
			season(1, "alternate", "Original Run"),
			season(2, "alternate", "Original Run"),
		}
		got := availableSeriesOrderings(seasons)
		if len(got) != 2 {
			t.Fatalf("expected 2 orderings, got %d: %+v", len(got), got)
		}
		// First-seen order preserved: official, then alternate.
		if got[0].Type != "official" || !got[0].IsOfficial {
			t.Fatalf("expected official first, got %+v", got[0])
		}
		if got[0].SeasonCount != 3 {
			t.Fatalf("expected 3 official seasons (specials excluded), got %d", got[0].SeasonCount)
		}
		if got[1].Type != "alternate" || got[1].IsOfficial {
			t.Fatalf("expected alternate second non-official, got %+v", got[1])
		}
		if got[1].Name != "Original Run" {
			t.Fatalf("expected display name from type.Name, got %q", got[1].Name)
		}
		if got[1].SeasonCount != 2 {
			t.Fatalf("expected 2 alternate seasons, got %d", got[1].SeasonCount)
		}
	})
}

func TestResolveSeasonType(t *testing.T) {
	available := []models.SeriesOrdering{
		{Type: "official", IsOfficial: true},
		{Type: "dvd"},
	}
	cases := []struct {
		name      string
		requested string
		want      string
	}{
		{"empty falls back to primary", "", "official"},
		{"valid requested honored", "dvd", "dvd"},
		{"case-insensitive", "DVD", "dvd"},
		{"unknown falls back to primary", "absolute", "official"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSeasonType(tc.requested, "official", available); got != tc.want {
				t.Fatalf("resolveSeasonType(%q) = %q, want %q", tc.requested, got, tc.want)
			}
		})
	}
}
