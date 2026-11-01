package debrid

import (
	"strings"

	"novastream/services/peartube"
)

// NewPearTubeScraper validates the relay URL via peartube.New and delegates
// search to PearTube's Stremio /stream endpoint so results carry resolution
// tiers (2160p/1080p), exact videoSize, and same-origin /v1/blob URLs.
func NewPearTubeScraper(relayURL, secret, name string) (Scraper, error) {
	client, err := peartube.New(relayURL, secret)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = "PearTube"
	}
	return NewDirectStremioScraper(client.BaseURL(), name, nil), nil
}
