package httpheaders

import (
	"net/http"
	"strings"
)

const (
	UserAgent = "mediastorm/1.0"
	// NZBDownloadUserAgent mimics a current SABnzbd release because some
	// indexers reject NZB downloads from unknown or outdated clients. Keep it
	// in step with upstream releases; per-indexer overrides take precedence.
	NZBDownloadUserAgent = "SABnzbd/5.1.3"

	acceptIndexerSearch = "application/xml, application/rss+xml, text/xml, */*"
	acceptNZBDownload   = "application/x-nzb, application/xml, text/xml, */*"
)

// SetIndexerSearchHeaders sets search headers; a non-empty userAgent overrides
// the default.
func SetIndexerSearchHeaders(req *http.Request, userAgent string) {
	req.Header.Set("User-Agent", pick(userAgent, UserAgent))
	req.Header.Set("Accept", acceptIndexerSearch)
}

// SetNZBDownloadHeaders sets NZB download headers; a non-empty userAgent
// overrides the default.
func SetNZBDownloadHeaders(req *http.Request, userAgent string) {
	req.Header.Set("User-Agent", pick(userAgent, NZBDownloadUserAgent))
	req.Header.Set("Accept", acceptNZBDownload)
}

func pick(override, fallback string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	return fallback
}
