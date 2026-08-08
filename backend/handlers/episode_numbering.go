package handlers

import "novastream/models"

// releaseAbsoluteEpisodeNumber is kept as a handler-local compatibility wrapper
// around the domain numbering contract shared with metadata.
func releaseAbsoluteEpisodeNumber(seasons []models.SeriesSeason, target models.SeriesEpisode) int {
	return models.ReleaseAbsoluteEpisodeNumber(seasons, target)
}
