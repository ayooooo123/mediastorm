package models

var releaseNumberedSpecialTVDBIDs = map[int64]struct{}{
	// One Piece episode 590 is filed under season zero by TVDB, but it is part
	// of the canonical release numbering. Excluding it shifts every later
	// release down by one (for example S23E18 becomes 1172 instead of 1173).
	4543896: {},
}

// ReleaseAbsoluteEpisodeNumber returns the episode number used by release
// names. Episodes from positive-numbered seasons contribute to the running
// total, along with the small set of season-zero episodes that release groups
// canonically number as regular episodes. Other provider specials remain
// excluded because they would shift later release numbers incorrectly.
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

	for _, season := range seasons {
		if season.Number != 0 {
			continue
		}
		for _, special := range season.Episodes {
			if releaseNumberedSpecialPrecedesTarget(special, target) {
				absolute++
			}
		}
	}
	return absolute
}

func releaseNumberedSpecialPrecedesTarget(special, target SeriesEpisode) bool {
	if _, ok := releaseNumberedSpecialTVDBIDs[special.TVDBID]; !ok {
		return false
	}
	if special.AiredDate != "" && target.AiredDate != "" {
		return special.AiredDate < target.AiredDate
	}
	return special.AbsoluteEpisodeNumber > 0 && target.AbsoluteEpisodeNumber > special.AbsoluteEpisodeNumber
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
