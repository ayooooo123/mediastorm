package debrid

import (
	"context"
	"log"
	"strings"

	"novastream/internal/requestsecurity"
	"novastream/models"
	"novastream/services/peartube"
)

// PearTubeScraper finds a title on a PearTube relay by IMDb id. Every result is
// a direct, Range-capable stream on the relay's blob server.
type PearTubeScraper struct {
	client *peartube.Client
	name   string
}

// NewPearTubeScraper builds a scraper for the relay at relayURL.
func NewPearTubeScraper(relayURL, secret, name string) (*PearTubeScraper, error) {
	client, err := peartube.New(relayURL, secret)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = "PearTube"
	}
	return &PearTubeScraper{client: client, name: name}, nil
}

func (p *PearTubeScraper) Name() string { return p.name }

// Search looks the title up by `imdb:<tt>` or `imdb:<tt>:sXXeYY`.
func (p *PearTubeScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	season, episode := 0, 0
	if req.Parsed.MediaType == MediaTypeSeries {
		if req.Parsed.Season <= 0 || req.Parsed.Episode <= 0 {
			return nil, nil
		}
		season, episode = req.Parsed.Season, req.Parsed.Episode
	}
	id := peartube.MediaID(req.IMDBID, season, episode)
	if id == "" {
		return nil, nil
	}
	found, err := p.client.Search(ctx, id)
	if err != nil {
		return nil, err
	}
	results := make([]ScrapeResult, 0, len(found))
	for _, item := range found {
		if item.StreamURL == "" {
			continue
		}
		results = append(results, ScrapeResult{
			Title:      item.Title,
			Indexer:    p.Name(),
			TorrentURL: item.StreamURL,
			SizeBytes:  item.Size,
			Provider:   "peartube",
			MetaID:     req.IMDBID,
			Source:     p.Name(),
			Attributes: map[string]string{
				"scraper":     "peartube",
				"preresolved": "true",
				"stream_url":  item.StreamURL,
				"raw_title":   item.Title,
			},
			ServiceType: models.ServiceTypeDebrid,
		})
	}
	log.Printf("[peartube] %s: %d result(s) for %s from %s", p.Name(), len(results), id, requestsecurity.URLForLog(p.client.BaseURL()))
	return results, nil
}
