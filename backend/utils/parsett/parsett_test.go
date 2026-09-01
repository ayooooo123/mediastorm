package parsett

import (
	"testing"
)

func TestParseTitle(t *testing.T) {
	testCases := []struct {
		name          string
		input         string
		expectedTitle string
		expectedYear  int
	}{
		{
			name:          "Movie with year and quality",
			input:         "The.Matrix.1999.1080p.BluRay.x264-SPARKS",
			expectedTitle: "The Matrix",
			expectedYear:  1999,
		},
		{
			name:          "TV Show with season and episode",
			input:         "The.Simpsons.S01E01.1080p.BluRay.x265.HEVC.10bit.AAC.5.1.Tigole",
			expectedTitle: "The Simpsons",
			expectedYear:  0, // No year in this title
		},
		{
			name:          "Simple movie title",
			input:         "Inception.2010.720p.BluRay.x264",
			expectedTitle: "Inception",
			expectedYear:  2010,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ParseTitle(tc.input)
			if err != nil {
				t.Fatalf("ParseTitle failed: %v", err)
			}

			if result.Title != tc.expectedTitle {
				t.Errorf("Expected title '%s', got '%s'", tc.expectedTitle, result.Title)
			}

			if result.Year != tc.expectedYear {
				t.Errorf("Expected year %d, got %d", tc.expectedYear, result.Year)
			}

			// Log the full result for inspection
			t.Logf("Full result: %+v", result)
		})
	}
}

func TestParseTitlePreservesCountry(t *testing.T) {
	result, err := ParseTitle("Shameless.UK.S01E01.DVDRip.x264")
	if err != nil {
		t.Fatalf("ParseTitle failed: %v", err)
	}
	if result.Country != "UK" {
		t.Fatalf("Country = %q, want UK", result.Country)
	}
}

func TestParseTitleIgnoresNikt0ReleaseGroupAsSeasonZero(t *testing.T) {
	for _, title := range []string{
		"Batman Death in the Family 2020 1080p BluRay x264-nikt0",
		"Batman.Death.in.the.Family.2020.1080p.BluRay.x264-nikt0",
	} {
		parsed, err := ParseTitle(title)
		if err != nil {
			t.Fatalf("ParseTitle(%q): %v", title, err)
		}
		if len(parsed.Seasons) != 0 {
			t.Fatalf("ParseTitle(%q) seasons = %v, want none", title, parsed.Seasons)
		}
	}
}

func TestParseTitlePreservesExplicitSpecialWithNikt0Group(t *testing.T) {
	parsed, err := ParseTitle("Example.Show.S00E01.1080p.WEB-DL.x264-nikt0")
	if err != nil {
		t.Fatalf("ParseTitle: %v", err)
	}
	if len(parsed.Seasons) != 1 || parsed.Seasons[0] != 0 || len(parsed.Episodes) != 1 || parsed.Episodes[0] != 1 {
		t.Fatalf("season/episode = %v/%v, want [0]/[1]", parsed.Seasons, parsed.Episodes)
	}
}
