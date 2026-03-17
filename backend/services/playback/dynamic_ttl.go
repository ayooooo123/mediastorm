package playback

import (
	"time"
)

// DynamicTTL computes TTL based on distance from air date (V-shaped curve).
// Uses airDateTimeUTC (RFC3339) if available, falls back to airDate (YYYY-MM-DD),
// then year+mediaType, then a 30-minute default.
func DynamicTTL(airDate string, airDateTimeUTC string, year int, mediaType string) time.Duration {
	now := time.Now()

	// Try to parse precise air time first (RFC3339)
	if airDateTimeUTC != "" {
		if t, err := time.Parse(time.RFC3339, airDateTimeUTC); err == nil {
			return ttlFromDistance(now.Sub(t))
		}
	}

	// Fall back to date-only (assume noon UTC as midpoint)
	if airDate != "" {
		if t, err := time.Parse("2006-01-02", airDate); err == nil {
			midday := t.Add(12 * time.Hour)
			return ttlFromDistance(now.Sub(midday))
		}
	}

	// Fall back to year + media type
	if year > 0 {
		currentYear := now.Year()
		if year >= currentYear {
			return 1 * time.Hour // Current year content
		}
		return 24 * time.Hour // Older content
	}

	// No date info at all
	return 1 * time.Hour
}

// ttlFromDistance returns TTL based on signed distance from air date.
// Negative = before air date, positive = after air date.
//
//	>7 days before → 1h before:  12h
//	 1h before → 2h after:       15min
//	 2h after → 24h after:       1h
//	>24h after:                   24h
func ttlFromDistance(distance time.Duration) time.Duration {
	hours := distance.Hours()

	if hours < -1 { // More than 1 hour before airtime
		return 12 * time.Hour
	}
	if hours <= 2 { // 1h before to 2h after airtime
		return 15 * time.Minute
	}
	if hours <= 24 { // 2h to 24h after airtime
		return 1 * time.Hour
	}
	// > 24h after airtime
	return 24 * time.Hour
}

// DynamicTTL is a convenience method on PrequeueEntry that extracts fields
// and delegates to the package-level DynamicTTL function.
func (e *PrequeueEntry) DynamicTTL() time.Duration {
	var airDate, airDateTimeUTC string
	if e.TargetEpisode != nil {
		airDate = e.TargetEpisode.AirDate
		airDateTimeUTC = e.TargetEpisode.AirDateTimeUTC
	}
	return DynamicTTL(airDate, airDateTimeUTC, e.Year, e.MediaType)
}
