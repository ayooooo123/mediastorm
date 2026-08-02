package peartube

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"novastream/models"
)

// IndexerName is what a p2p result reports as its origin. The stream picker
// shows it verbatim.
const IndexerName = "PearTube"

// ProviderName is the `provider` attribute every p2p result carries, mirroring
// how debrid results name their resolver.
const ProviderName = "peartube"

// A catalog entity id encodes the TMDB coordinates the publisher used. Both
// forms the relay produces are matched by scanning rather than anchoring, so a
// namespaced id (`tmdb:movie:603`, `tmdb:episode:show:1399:s1:e1`) and a bare
// one (`movie:603`, `show:1399:s1:e1`) both resolve to the same coordinates.
var (
	movieEntityID   = regexp.MustCompile(`(?i)\bmovie:([1-9][0-9]{0,9})\b`)
	episodeEntityID = regexp.MustCompile(`(?i)\bshow:([1-9][0-9]{0,9}):s([0-9]{1,4}):e([0-9]{1,5})\b`)
)

// entityCoordinates are the TMDB coordinates recovered from a catalog entity id.
type entityCoordinates struct {
	TMDBID  string
	Season  int
	Episode int
	Kind    string // "movie" or "episode"
}

func parseEntityCoordinates(entityID string) (entityCoordinates, bool) {
	if match := episodeEntityID.FindStringSubmatch(entityID); match != nil {
		season, _ := strconv.Atoi(match[2])
		episode, _ := strconv.Atoi(match[3])
		if season > 0 && episode > 0 {
			return entityCoordinates{TMDBID: match[1], Season: season, Episode: episode, Kind: "episode"}, true
		}
	}
	if match := movieEntityID.FindStringSubmatch(entityID); match != nil {
		return entityCoordinates{TMDBID: match[1], Kind: "movie"}, true
	}
	return entityCoordinates{}, false
}

func sourceCoordinates(source CatalogSource) (entityCoordinates, bool) {
	if !strings.EqualFold(strings.TrimSpace(source.MediaProvider), "tmdb") {
		return entityCoordinates{}, false
	}
	id := strings.TrimSpace(source.MediaID)
	if id == "" {
		return entityCoordinates{}, false
	}
	switch source.ContentKind {
	case "movie":
		return entityCoordinates{TMDBID: id, Kind: "movie"}, true
	case "episode":
		if source.SeasonNumber > 0 && source.EpisodeNumber > 0 {
			return entityCoordinates{
				TMDBID: id, Season: source.SeasonNumber, Episode: source.EpisodeNumber, Kind: "episode",
			}, true
		}
	}
	return entityCoordinates{}, false
}

func coordinatesForSource(entity CatalogEntity, source CatalogSource) (entityCoordinates, bool) {
	if coordinates, ok := sourceCoordinates(source); ok {
		return coordinates, true
	}
	// Backward compatibility for relay catalogs produced before source
	// coordinates were explicit.
	return parseEntityCoordinates(entity.EntityID)
}

// SearchRequest is what the indexer pipeline knows about the title a user is
// trying to play.
type SearchRequest struct {
	Title      string
	Year       int
	Season     int
	Episode    int
	MediaType  string // "movie" or "series"
	TMDBID     string // optional; when present it is the authoritative match key
	MaxResults int
}

func (r SearchRequest) wantsEpisode() bool {
	return r.Season > 0 && r.Episode > 0
}

// Search maps every relay catalog entity that matches the request onto the
// result struct the stream pipeline ranks and plays.
//
// A relay that refuses to enumerate (open access not enabled) yields no results
// and ErrRelayNotOpen, which callers are expected to treat as "this source is
// not available yet" rather than as a search failure.
func (c *Client) Search(ctx context.Context, req SearchRequest) ([]models.NZBResult, error) {
	if c == nil {
		return nil, nil
	}
	entities, err := c.Catalog(ctx)
	if err != nil {
		return nil, err
	}

	var results []models.NZBResult
	for _, entity := range entities {
		for _, source := range entity.Sources {
			if !matches(entity, source, req) {
				continue
			}
			if source.PublicationID == "" || source.RenditionID == "" {
				// A source without a rendition cannot be addressed by the
				// stream endpoint, so offering it would hand the player a URL
				// that resolves to nothing.
				continue
			}
			results = append(results, c.buildResult(entity, source, req))
			if req.MaxResults > 0 && len(results) >= req.MaxResults {
				log.Printf("[peartube] search %q: truncated at %d results", req.Title, len(results))
				return results, nil
			}
		}
	}
	log.Printf("[peartube] search %q (year=%d s%02de%02d tmdb=%q): %d of %d catalog entities matched",
		req.Title, req.Year, req.Season, req.Episode, req.TMDBID, len(results), len(entities))
	return results, nil
}

func matches(entity CatalogEntity, source CatalogSource, req SearchRequest) bool {
	coords, hasCoords := coordinatesForSource(entity, source)

	if hasCoords && req.TMDBID != "" {
		if coords.TMDBID != req.TMDBID {
			return false
		}
		if req.wantsEpisode() {
			return coords.Season == req.Season && coords.Episode == req.Episode
		}
		return coords.Kind == "movie"
	}

	if !titlesMatch(entity.Title, req.Title) {
		return false
	}
	if req.wantsEpisode() {
		// Without coordinates there is no way to tell which episode of a show
		// this entity holds, and serving the wrong episode is worse than
		// serving none.
		return hasCoords && coords.Season == req.Season && coords.Episode == req.Episode
	}
	if hasCoords && coords.Kind == "episode" {
		return false
	}
	// A one-year drift between TMDB's release year and a publisher's is common
	// enough that an exact match would lose real sources.
	if req.Year > 0 && entity.Year > 0 && absDiff(req.Year, entity.Year) > 1 {
		return false
	}
	return true
}

func (c *Client) buildResult(entity CatalogEntity, source CatalogSource, req SearchRequest) models.NZBResult {
	streamURL := c.StreamURL(source.PublicationID, source.RenditionID)
	title := strings.TrimSpace(entity.Title)
	if title == "" {
		title = strings.TrimSpace(req.Title)
	}
	if coords, ok := coordinatesForSource(entity, source); ok && coords.Kind == "episode" {
		title = fmt.Sprintf("%s S%02dE%02d", title, coords.Season, coords.Episode)
	} else if entity.Year > 0 {
		title = fmt.Sprintf("%s %d", title, entity.Year)
	}
	// The publisher key disambiguates two peers serving the same title in a
	// list that otherwise shows one identical row per source.
	if len(source.PublisherID) >= 8 {
		title = fmt.Sprintf("%s [PearTube %s]", title, source.PublisherID[:8])
	} else {
		title = title + " [PearTube]"
	}

	attributes := map[string]string{
		"provider":      ProviderName,
		"stream_url":    streamURL,
		"publicationId": source.PublicationID,
		"renditionId":   source.RenditionID,
	}
	if entity.EntityID != "" {
		attributes["entityId"] = entity.EntityID
	}
	if source.PublisherID != "" {
		attributes["publisherId"] = source.PublisherID
	}
	if source.CoreKey != "" {
		attributes["coreKey"] = source.CoreKey
	}
	if source.CoreLength > 0 {
		attributes["coreLength"] = strconv.FormatInt(source.CoreLength, 10)
	}

	// No resolution, codec, seeder count, or cache state: a PearTube catalog
	// entry declares none of them, and inventing them would corrupt ranking
	// with numbers nobody measured.
	return models.NZBResult{
		Title:       title,
		Indexer:     IndexerName,
		GUID:        "peartube:" + source.PublicationID + ":" + source.RenditionID,
		Link:        streamURL,
		DownloadURL: streamURL,
		SizeBytes:   source.ByteLength,
		ServiceType: models.ServiceTypeP2P,
		Attributes:  attributes,
	}
}

func titlesMatch(left, right string) bool {
	normalizedLeft := normalizeTitle(left)
	normalizedRight := normalizeTitle(right)
	return normalizedLeft != "" && normalizedLeft == normalizedRight
}

// normalizeTitle reduces a display title to comparable letters and digits, so
// punctuation and separator drift between TMDB and a publisher do not defeat a
// title match.
func normalizeTitle(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
