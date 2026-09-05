package indexer

import (
	"novastream/config"
	"novastream/models"
	"strings"
	"time"
)

func buildAdaptiveSearchSummary(f models.FilterSettings, settings config.Settings, opts SearchOptions) models.AdaptiveSearchSummary {
	summary := models.AdaptiveSearchSummary{Enabled: models.BoolVal(f.AdaptivePlaybackEnabled, settings.Filtering.AdaptivePlaybackEnabled)}
	caps := models.ComputeAdaptiveCaps(summary.Enabled, models.FloatVal(f.AdaptiveTargetBufferFactor, settings.Filtering.AdaptiveTargetBufferFactor), models.AdaptiveSettingsForRequest(nil, opts.AdaptiveThroughput), time.Now())
	summary.MaxSizeGB = caps.MaxSizeEpisodeGB
	if strings.EqualFold(opts.MediaType, "movie") {
		summary.MaxSizeGB = caps.MaxSizeMovieGB
	}
	if summary.MaxSizeGB != nil && opts.AdaptiveThroughput != nil {
		summary.MeasuredMbps = opts.AdaptiveThroughput.MeasuredMbps
		summary.MeasuredAt = opts.AdaptiveThroughput.MeasuredAt
	}
	return summary
}
