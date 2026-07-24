package debrid

import (
	"net/url"
	"path"
	"strings"
)

// knownPlaceholderFilenames are tiny non-media assets returned by some stream gateways
// when content is unavailable or still being prepared.
var knownPlaceholderFilenames = map[string]struct{}{
	"download_failed.mp4": {},
	"downloading.mp4":     {},
}

var knownPlaceholderHosts = map[string]struct{}{
	"slate.elfhosted.com": {},
}

// IsKnownPlaceholderURL returns true when the URL points to a known placeholder asset.
func IsKnownPlaceholderURL(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false
	}

	parsed, err := url.Parse(trimmed)
	if err == nil {
		if _, ok := knownPlaceholderHosts[strings.ToLower(parsed.Hostname())]; ok {
			return true
		}
		name := strings.ToLower(path.Base(parsed.Path))
		_, ok := knownPlaceholderFilenames[name]
		return ok
	}

	// Fallback for malformed URLs: use suffix matching.
	lowered := strings.ToLower(trimmed)
	return strings.Contains(lowered, "/download_failed.mp4") || strings.Contains(lowered, "/downloading.mp4")
}

// IsKnownPlaceholderResponse catches status playlists whose initial provider URL
// hides the eventual placeholder host. ElfHosted's Comet instance returns these
// HLS slates for uncached debrid items.
func IsKnownPlaceholderResponse(finalURL string, body []byte) bool {
	if IsKnownPlaceholderURL(finalURL) {
		return true
	}

	lowered := strings.ToLower(string(body))
	return strings.Contains(lowered, "slate.elfhosted.com/") ||
		strings.Contains(lowered, "media_not_cached_yet")
}
