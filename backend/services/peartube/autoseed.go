package peartube

import (
	"context"
	"fmt"
	"strings"
)

// AutoSeedEnv is the environment default for the switch that turns
// watch-triggered seeding off. The stored admin setting wins over it; see
// config.PearTubeSettings for the precedence rule and Resolve for the code.
//
// Precedence, outermost first:
//
//  1. The relay URL decides whether the integration exists at all. Without a
//     relay there is nothing to seed to, and no value of this switch can change
//     that. That is what keeps an install which never asked for p2p behaving
//     exactly as it did before: no relay, no seeding, no new calls.
//  2. A false value (0, false, no, off) disables the automatic trigger. The
//     manual seed endpoint keeps working either way.
//  3. Anything else, unset included, enables it. An operator who configured a
//     relay wants the swarm to grow, so contributing what they watch is the
//     useful default rather than a setting to discover.
const AutoSeedEnv = "PEARTUBE_AUTOSEED"

// EntityKey is the swarm's identity for a title, derived from the coordinates a
// seed would publish it under. One key serves two purposes that must agree:
// asking whether the relay already carries a title, and claiming it so it is
// only seeded once.
//
// The form mirrors the coordinates a relay entity id encodes, which is what
// makes the two comparable: `movie:603`, `show:1399:s1:e1`. Coordinates that
// cannot be published — no TMDB id, an episode without season and episode
// numbers — have no key.
func EntityKey(coords ArchiveCoordinates) string {
	tmdbID := strings.TrimSpace(coords.TMDBID)
	if tmdbID == "" {
		return ""
	}
	switch coords.ContentKind {
	case "movie":
		return "movie:" + tmdbID
	case "episode":
		if coords.TMDBSeason < 1 || coords.TMDBEpisode < 1 {
			return ""
		}
		return fmt.Sprintf("show:%s:s%d:e%d", tmdbID, coords.TMDBSeason, coords.TMDBEpisode)
	default:
		return ""
	}
}

// catalogEntityKey recovers the same key from a relay catalog entity id, so a
// published entity and a pending seed are compared on identical terms.
func catalogEntityKey(entityID string) string {
	coords, ok := parseEntityCoordinates(entityID)
	if !ok {
		return ""
	}
	return EntityKey(ArchiveCoordinates{
		ContentKind: coords.Kind,
		TMDBID:      coords.TMDBID,
		TMDBSeason:  coords.Season,
		TMDBEpisode: coords.Episode,
	})
}

// CatalogHasEntity reports whether the swarm can already serve these
// coordinates. It reads the same briefly-cached catalog a search reads, so a
// watch that follows a search costs no round trip.
//
// An entity with no addressable source does not count as published: the stream
// endpoint could not serve it, which is the situation seeding exists to fix.
//
// A relay that is slow, unreachable, or refusing to enumerate returns the error
// rather than false. "I could not find out" is not "it is missing", and the
// caller must not turn a catalog timeout into a duplicate fetch of a whole file.
func (c *Client) CatalogHasEntity(ctx context.Context, coords ArchiveCoordinates) (bool, error) {
	if c == nil {
		return false, nil
	}
	key := EntityKey(coords)
	if key == "" {
		return false, nil
	}
	entities, err := c.Catalog(ctx)
	if err != nil {
		return false, err
	}
	for _, entity := range entities {
		if catalogEntityKey(entity.EntityID) != key {
			continue
		}
		for _, source := range entity.Sources {
			if source.PublicationID != "" && source.RenditionID != "" {
				return true, nil
			}
		}
	}
	return false, nil
}
