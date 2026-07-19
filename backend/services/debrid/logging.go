package debrid

import (
	"strings"

	"novastream/internal/requestsecurity"
)

func safeURLForLog(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(rawURL), "magnet:") {
		return "magnet:[redacted]"
	}
	if !strings.Contains(rawURL, "://") {
		if strings.ContainsAny(rawURL, "?&") {
			return "[redacted-url]"
		}
		return rawURL
	}
	return requestsecurity.URLForLog(rawURL)
}
