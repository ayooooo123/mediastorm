package jellyfin

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeServerURL returns a canonical Jellyfin base URL. Jellyfin commonly
// runs over HTTP on a LAN, so addresses entered as host:port default to HTTP.
func NormalizeServerURL(serverURL string) (string, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return "", fmt.Errorf("server URL is required")
	}
	if !strings.Contains(serverURL, "://") {
		serverURL = "http://" + strings.TrimLeft(serverURL, "/")
	}

	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("server URL must use http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("server URL must include a host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("server URL must not include a query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}
