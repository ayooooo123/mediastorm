package models

import "testing"

func TestNormalizeReleaseAbsoluteEpisodeNumbersStargateMultipartPilot(t *testing.T) {
	details := &SeriesDetails{Seasons: []SeriesSeason{
		{
			Number:       1,
			EpisodeCount: 9,
			Episodes: []SeriesEpisode{
				{Name: "Children of the Gods (1)", SeasonNumber: 1, EpisodeNumber: 1},
				{Name: "Children of the Gods (2)", SeasonNumber: 1, EpisodeNumber: 2},
				{Name: "The Nox", SeasonNumber: 1, EpisodeNumber: 8, AbsoluteEpisodeNumber: 7},
				{Name: "Brief Candle", SeasonNumber: 1, EpisodeNumber: 9, AbsoluteEpisodeNumber: 8},
			},
		},
	}}

	if !NormalizeReleaseAbsoluteEpisodeNumbers(details) {
		t.Fatal("expected provider absolute numbers to be normalized")
	}
	if got := details.Seasons[0].Episodes[2].AbsoluteEpisodeNumber; got != 8 {
		t.Fatalf("The Nox absolute = %d, want 8", got)
	}
	if got := details.Seasons[0].Episodes[3].AbsoluteEpisodeNumber; got != 9 {
		t.Fatalf("Brief Candle absolute = %d, want 9", got)
	}
}

func TestNormalizeReleaseAbsoluteEpisodeNumbersExcludesSpecials(t *testing.T) {
	details := &SeriesDetails{Seasons: []SeriesSeason{
		{
			Number:       0,
			EpisodeCount: 1,
			Episodes: []SeriesEpisode{
				{SeasonNumber: 0, EpisodeNumber: 1, AbsoluteEpisodeNumber: 13},
			},
		},
		{Number: 1, EpisodeCount: 12},
		{
			Number:       2,
			EpisodeCount: 11,
			Episodes: []SeriesEpisode{
				{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteEpisodeNumber: 14},
			},
		},
	}}

	NormalizeReleaseAbsoluteEpisodeNumbers(details)
	if got := details.Seasons[2].Episodes[0].AbsoluteEpisodeNumber; got != 13 {
		t.Fatalf("S02E01 absolute = %d, want 13", got)
	}
	if got := details.Seasons[0].Episodes[0].AbsoluteEpisodeNumber; got != 13 {
		t.Fatalf("special absolute changed to %d, want provider value 13 preserved", got)
	}
}

func TestNormalizeReleaseAbsoluteEpisodeNumbersIncludesCanonicallyNumberedSpecial(t *testing.T) {
	details := &SeriesDetails{
		Seasons: []SeriesSeason{
			{
				Number: 0,
				Episodes: []SeriesEpisode{
					{
						TVDBID:                4543896,
						SeasonNumber:          0,
						EpisodeNumber:         39,
						AbsoluteEpisodeNumber: 590,
						AiredDate:             "2013-04-07",
					},
				},
			},
			{Number: 1, EpisodeCount: 589},
			{
				Number:       2,
				EpisodeCount: 2,
				Episodes: []SeriesEpisode{
					{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteEpisodeNumber: 591, AiredDate: "2013-04-14"},
					{SeasonNumber: 2, EpisodeNumber: 2, AbsoluteEpisodeNumber: 592, AiredDate: "2013-04-21"},
				},
			},
		},
	}

	if NormalizeReleaseAbsoluteEpisodeNumbers(details) {
		t.Fatal("canonically numbered special should preserve the provider release numbering")
	}
	if got := details.Seasons[2].Episodes[0].AbsoluteEpisodeNumber; got != 591 {
		t.Fatalf("first regular episode after numbered special = %d, want 591", got)
	}
	if got := details.Seasons[2].Episodes[1].AbsoluteEpisodeNumber; got != 592 {
		t.Fatalf("second regular episode after numbered special = %d, want 592", got)
	}
}
