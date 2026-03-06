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

// IsKnownPlaceholderURL returns true when the URL points to a known placeholder asset.
func IsKnownPlaceholderURL(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false
	}

	parsed, err := url.Parse(trimmed)
	if err == nil {
		name := strings.ToLower(path.Base(parsed.Path))
		_, ok := knownPlaceholderFilenames[name]
		return ok
	}

	// Fallback for malformed URLs: use suffix matching.
	lowered := strings.ToLower(trimmed)
	return strings.Contains(lowered, "/download_failed.mp4") || strings.Contains(lowered, "/downloading.mp4")
}
