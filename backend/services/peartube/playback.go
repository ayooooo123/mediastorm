package peartube

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"novastream/models"
)

// HealthStatus is what a p2p resolution reports instead of a health verdict.
//
// Usenet answers "healthy" after sampling segments; debrid answers "cached"
// after asking a provider whether it holds the torrent. A PearTube rendition
// has neither notion: the relay serves whatever has replicated and streams the
// rest as it arrives, so there is no upstream state to interrogate. Reporting
// "healthy" or "cached" here would be an answer to a question nobody asked.
const HealthStatus = "p2p"

// ResolvePlayback turns a p2p search result into a playback resolution.
//
// There is nothing to resolve in the usenet or debrid sense: the search result
// already names the publication and rendition, and the relay's stream endpoint
// is range-capable, so the direct URL is the playback path. The video layer
// already proxies absolute http(s) stream paths.
func ResolvePlayback(relay *Client, candidate models.NZBResult) (*models.PlaybackResolution, error) {
	if relay == nil {
		return nil, errors.New("peartube relay is not configured")
	}

	streamURL := strings.TrimSpace(candidate.Attributes["stream_url"])
	if streamURL == "" {
		publicationID := strings.TrimSpace(candidate.Attributes["publicationId"])
		renditionID := strings.TrimSpace(candidate.Attributes["renditionId"])
		if publicationID != "" && renditionID != "" {
			streamURL = relay.StreamURL(publicationID, renditionID)
		}
	}
	if streamURL == "" {
		streamURL = strings.TrimSpace(candidate.DownloadURL)
	}
	if streamURL == "" {
		streamURL = strings.TrimSpace(candidate.Link)
	}
	if streamURL == "" {
		return nil, errors.New("p2p result carries no publication or rendition to stream")
	}
	// A p2p result reaches this point straight from a request body, so the URL
	// it names is only trustworthy once it is confirmed to address the relay
	// this server was configured with.
	if !relay.OwnsURL(streamURL) {
		return nil, fmt.Errorf("p2p stream URL does not address the configured relay")
	}

	log.Printf("[peartube] resolved p2p playback title=%q publication=%q rendition=%q",
		strings.TrimSpace(candidate.Title),
		candidate.Attributes["publicationId"],
		candidate.Attributes["renditionId"])

	return &models.PlaybackResolution{
		WebDAVPath:    streamURL,
		HealthStatus:  HealthStatus,
		FileSize:      candidate.SizeBytes,
		SourceNZBPath: streamURL,
	}, nil
}
