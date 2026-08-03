package debrid

import "strings"

const (
	directCacheStatusAttribute   = "sourceCacheStatus"
	directCacheEvidenceAttribute = "sourceCacheEvidence"
	directCacheLikelyCached      = "likely_cached"
	directCacheLikelyUncached    = "likely_uncached"
)

// annotateDirectStreamCacheHint normalizes cache hints embedded in the display
// labels returned by Stremio-style direct-stream providers. These values are
// diagnostic hints only; playback health checks remain authoritative.
func annotateDirectStreamCacheHint(attributes map[string]string) {
	if attributes == nil || attributes["preresolved"] != "true" {
		return
	}

	scraper := strings.ToLower(strings.TrimSpace(attributes["scraper"]))
	switch scraper {
	case "comet", "mediafusion", "aiostreams":
	default:
		return
	}

	label := strings.TrimSpace(attributes["label"])
	if label == "" {
		label = strings.TrimSpace(attributes["raw_name"])
	}
	if label == "" {
		return
	}

	// Variation selectors change emoji presentation but not meaning.
	normalized := strings.ReplaceAll(label, "\ufe0f", "")
	hasCachedMarker := strings.Contains(normalized, "⚡")
	hasUncachedMarker := strings.Contains(normalized, "⬇")
	if hasCachedMarker == hasUncachedMarker {
		return
	}

	if hasCachedMarker {
		attributes[directCacheStatusAttribute] = directCacheLikelyCached
	} else {
		attributes[directCacheStatusAttribute] = directCacheLikelyUncached
	}
	attributes[directCacheEvidenceAttribute] = "provider_label"
}
