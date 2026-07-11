package debrid

import (
	"net/url"
	"strings"
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
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "[redacted-url]"
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return rawURL
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
