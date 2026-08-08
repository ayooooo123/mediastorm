package models

// ReleaseAbsoluteEpisodeNumber returns the episode number used by release
// names: only episodes from positive-numbered seasons contribute to the
// running total. Provider absolute numbering can group multipart episodes or
// include season-zero specials, so it is not safe to expose directly as the
// release absolute number.
func ReleaseAbsoluteEpisodeNumber(seasons []SeriesSeason, target SeriesEpisode) int {
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

// NormalizeReleaseAbsoluteEpisodeNumbers gives every regular episode the same
// absolute-numbering semantics used by release matching and prequeue. Season
// zero is left untouched because specials do not have a regular release
// absolute position.
func NormalizeReleaseAbsoluteEpisodeNumbers(details *SeriesDetails) bool {
	if details == nil {
		return false
	}

	changed := false
	for seasonIndex := range details.Seasons {
		season := &details.Seasons[seasonIndex]
		if season.Number <= 0 {
			continue
		}
		for episodeIndex := range season.Episodes {
			episode := &season.Episodes[episodeIndex]
			releaseAbsolute := ReleaseAbsoluteEpisodeNumber(details.Seasons, *episode)
			if releaseAbsolute <= 0 || episode.AbsoluteEpisodeNumber == releaseAbsolute {
				continue
			}
			episode.AbsoluteEpisodeNumber = releaseAbsolute
			changed = true
		}
	}
	return changed
}
