package handlers

import (
	"testing"

	"novastream/models"
)

func TestReleaseAbsoluteEpisodeNumberIgnoresSpecials(t *testing.T) {
	seasons := []models.SeriesSeason{
		{
			Number:       0,
			EpisodeCount: 1,
			Episodes: []models.SeriesEpisode{
				{SeasonNumber: 0, EpisodeNumber: 1, AbsoluteEpisodeNumber: 13},
			},
		},
		{Number: 1, EpisodeCount: 12},
		{
			Number:       2,
			EpisodeCount: 11,
			Episodes: []models.SeriesEpisode{
				{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteEpisodeNumber: 14},
			},
		},
	}

	target := seasons[2].Episodes[0]
	if got := releaseAbsoluteEpisodeNumber(seasons, target); got != 13 {
		t.Fatalf("releaseAbsoluteEpisodeNumber() = %d, want 13", got)
	}
}

func TestReleaseAbsoluteEpisodeNumberRequiresEveryPriorSeason(t *testing.T) {
	seasons := []models.SeriesSeason{
		{Number: 22, EpisodeCount: 100},
		{Number: 23, Episodes: []models.SeriesEpisode{{SeasonNumber: 23, EpisodeNumber: 7, AbsoluteEpisodeNumber: 1162}}},
	}

	if got := releaseAbsoluteEpisodeNumber(seasons, seasons[1].Episodes[0]); got != 0 {
		t.Fatalf("releaseAbsoluteEpisodeNumber() = %d, want 0 with incomplete prior seasons", got)
	}
}
