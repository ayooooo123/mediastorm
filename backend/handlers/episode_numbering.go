package handlers

import "novastream/models"

// releaseAbsoluteEpisodeNumber returns the absolute number used by anime
// release groups: regular episodes from positive-numbered seasons only.
// Provider absolute numbering can include season-zero specials and therefore
// shift every later regular episode (for example Kaiju No. 8 S02E01).
func releaseAbsoluteEpisodeNumber(seasons []models.SeriesSeason, target models.SeriesEpisode) int {
	if target.SeasonNumber <= 0 || target.EpisodeNumber <= 0 {
		return 0
	}

	seasonCounts := make(map[int]int, len(seasons))
	for _, season := range seasons {
		if season.Number <= 0 || season.Number >= target.SeasonNumber {
			continue
		}
		count := season.EpisodeCount
		if count <= 0 {
			count = len(season.Episodes)
		}
		if count > 0 {
			seasonCounts[season.Number] = count
		}
	}

	absolute := target.EpisodeNumber
	for seasonNumber := 1; seasonNumber < target.SeasonNumber; seasonNumber++ {
		count, ok := seasonCounts[seasonNumber]
		if !ok || count <= 0 {
			return 0
		}
		absolute += count
	}
	return absolute
}
