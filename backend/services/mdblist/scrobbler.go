package mdblist

import (
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

// UserService provides access to user profile data for scrobbling.
type UserService interface {
	Get(id string) (models.User, bool)
}

// Scrobbler implements history.TraktScrobbler for MDBList watched-item sync.
type Scrobbler struct {
	client        *ScrobbleClient
	configManager *config.Manager
	userService   UserService
}

// NewScrobbler creates a new MDBList scrobbler.
func NewScrobbler(client *ScrobbleClient, configManager *config.Manager) *Scrobbler {
	return &Scrobbler{
		client:        client,
		configManager: configManager,
	}
}

// SetUserService sets the user service for looking up profile MDBList account associations.
func (s *Scrobbler) SetUserService(userService UserService) {
	s.userService = userService
}

// IsEnabled returns whether any MDBList account is configured for scrobbling.
func (s *Scrobbler) IsEnabled() bool {
	settings, err := s.configManager.Load()
	if err != nil {
		return false
	}
	if !settings.MDBList.Enabled {
		return false
	}
	for _, account := range settings.MDBList.Accounts {
		if account.APIKey != "" {
			return true
		}
	}
	return false
}

// IsEnabledForUser returns whether MDBList scrobbling is enabled for a specific user
// (i.e., the user is linked to an MDBList account with a valid API key).
func (s *Scrobbler) IsEnabledForUser(userID string) bool {
	account := s.getAccountForUser(userID)
	return account != nil && account.APIKey != ""
}

// getAccountForUser returns the MDBList account associated with the given user, or nil if none.
func (s *Scrobbler) getAccountForUser(userID string) *config.MDBListAccount {
	if s.userService == nil {
		return nil
	}

	user, ok := s.userService.Get(userID)
	if !ok || user.MdblistAccountID == "" {
		return nil
	}

	settings, err := s.configManager.Load()
	if err != nil {
		return nil
	}

	return settings.MDBList.GetAccountByID(user.MdblistAccountID)
}

// ScrobbleMovie syncs a watched movie to MDBList for the given user.
func (s *Scrobbler) ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	account := s.getAccountForUser(userID)
	if account == nil || account.APIKey == "" {
		return nil
	}

	s.client.UpdateAPIKey(account.APIKey)

	item := SyncWatchedMovieItem{
		IDs: ScrobbleIDs{
			IMDB: imdbID,
			TMDB: tmdbID,
		},
		WatchedAt: watchedAt.UTC().Format(time.RFC3339),
	}

	log.Printf("[mdblist-scrobble] syncing watched movie for user %s (imdb=%s tmdb=%d)", userID, imdbID, tmdbID)
	return s.client.SyncWatched(SyncWatchedRequest{
		Movies: []SyncWatchedMovieItem{item},
	})
}

// ScrobbleEpisode syncs a watched episode to MDBList for the given user.
// Note: showTVDBID is not usable with MDBList (no TVDB support), so the show
// must be identified by IMDB/TMDB IDs from externalIDs (including titleId /
// seriesID-style values such as tmdb:tv:82782).
//
// Episode numbering: try pure seasonal first (S23E17). absoluteEpisode is a
// TVDB-style cumulative index present on many non-anime shows too (Succession
// S2E1 has absoluteEpisode=11), so it must NOT be used as the primary episode
// number. If seasonal is not_found and a distinct absolute exists, retry the
// rare MDBList hybrid form (seasonal season + absolute episode, e.g. One Piece
// S23E1172).
func (s *Scrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time, externalIDs map[string]string) error {
	account := s.getAccountForUser(userID)
	if account == nil || account.APIKey == "" {
		return nil
	}

	s.client.UpdateAPIKey(account.APIKey)

	ids := resolveShowScrobbleIDs(externalIDs)
	if ids.IMDB == "" && ids.TMDB == 0 {
		log.Printf("[mdblist-scrobble] episode sync/watched skipped for user %s (s%02de%02d) — no MDBList-compatible show IDs (tvdb=%d)",
			userID, season, episode, showTVDBID)
		return nil
	}

	watchedAtStr := watchedAt.UTC().Format(time.RFC3339)
	log.Printf("[mdblist-scrobble] syncing watched episode for user %s (imdb=%s tmdb=%d s%02de%02d)",
		userID, ids.IMDB, ids.TMDB, season, episode)

	result, err := s.client.SyncWatchedDetailed(syncWatchedEpisodeRequest(ids, season, episode, watchedAtStr))
	if err != nil {
		return err
	}
	if result.NotFoundEpisodes == 0 {
		return nil
	}

	absolute := absoluteEpisodeFromExternalIDs(externalIDs)
	if absolute <= 0 || absolute == episode {
		return nil
	}
	log.Printf("[mdblist-scrobble] seasonal s%02de%02d not found; retrying hybrid absolute e%d", season, episode, absolute)
	_, err = s.client.SyncWatchedDetailed(syncWatchedEpisodeRequest(ids, season, absolute, watchedAtStr))
	return err
}

func syncWatchedEpisodeRequest(ids ScrobbleIDs, season, episode int, watchedAt string) SyncWatchedRequest {
	return SyncWatchedRequest{
		Shows: []SyncWatchedShowItem{{
			IDs: ids,
			Seasons: []SyncWatchedSeason{{
				Number: season,
				Episodes: []SyncWatchedEpisode{{
					Number:    episode,
					WatchedAt: watchedAt,
				}},
			}},
		}},
	}
}

// BuildScrobbleRequest converts a PlaybackProgressUpdate to an MDBList ScrobbleRequest.
// Episodes always use pure seasonal numbering here. Callers that receive a 404
// "Episode not found" should retry with BuildScrobbleRequestHybrid when a
// distinct absoluteEpisode is available.
func BuildScrobbleRequest(update models.PlaybackProgressUpdate, percentWatched float64) ScrobbleRequest {
	return buildScrobbleRequest(update, percentWatched, false)
}

// BuildScrobbleRequestHybrid is like BuildScrobbleRequest but uses absolute
// episode numbering for MDBList hybrid catalogs (seasonal season + absolute ep).
func BuildScrobbleRequestHybrid(update models.PlaybackProgressUpdate, percentWatched float64) ScrobbleRequest {
	return buildScrobbleRequest(update, percentWatched, true)
}

func buildScrobbleRequest(update models.PlaybackProgressUpdate, percentWatched float64, hybridAbsolute bool) ScrobbleRequest {
	// MDBList requires at most 5 total digits in progress (e.g. 99.99, not 45.123456)
	req := ScrobbleRequest{
		Progress: math.Round(percentWatched*100) / 100,
	}

	if update.MediaType == "movie" {
		req.Movie = &ScrobbleMoviePayload{
			IDs: externalIDsToScrobbleIDs(update.ExternalIDs),
		}
	} else if update.MediaType == "episode" {
		episodeNumber := update.EpisodeNumber
		if hybridAbsolute {
			if absolute := absoluteEpisodeFromExternalIDs(update.ExternalIDs); absolute > 0 {
				episodeNumber = absolute
			}
		}
		req.Show = &ScrobbleShowPayload{
			IDs: seriesIDToScrobbleIDs(update.SeriesID, update.ExternalIDs),
			Season: &ScrobbleSeasonBlock{
				Number: update.SeasonNumber,
				Episode: &ScrobbleEpisodePayload{
					Number: episodeNumber,
				},
			},
		}
	}

	return req
}

// HybridEpisodeNumber returns absoluteEpisode when it differs from the seasonal
// episode number (candidate for MDBList hybrid retry). Returns 0 when hybrid
// retry is not applicable.
func HybridEpisodeNumber(seasonalEpisode int, externalIDs map[string]string) int {
	absolute := absoluteEpisodeFromExternalIDs(externalIDs)
	if absolute > 0 && absolute != seasonalEpisode {
		return absolute
	}
	return 0
}

func absoluteEpisodeFromExternalIDs(externalIDs map[string]string) int {
	if externalIDs == nil {
		return 0
	}
	raw := strings.TrimSpace(externalIDs["absoluteEpisode"])
	if raw == "" {
		return 0
	}
	absolute, err := strconv.Atoi(raw)
	if err != nil || absolute <= 0 {
		return 0
	}
	return absolute
}

func externalIDsToScrobbleIDs(extIDs map[string]string) ScrobbleIDs {
	ids := ScrobbleIDs{}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}
	// Note: MDBList does not recognize TVDB IDs — only IMDB and TMDB
	return ids
}

// resolveShowScrobbleIDs builds MDBList show IDs from external IDs, falling
// back to titleId (and any other seriesID-shaped values) when imdb/tmdb keys
// are missing — common for sparse progress rows that only store titleId.
func resolveShowScrobbleIDs(extIDs map[string]string) ScrobbleIDs {
	ids := externalIDsToScrobbleIDs(extIDs)
	if ids.TMDB != 0 || ids.IMDB != "" {
		return ids
	}
	if titleID := extIDs["titleId"]; titleID != "" {
		return seriesIDToScrobbleIDs(titleID, extIDs)
	}
	return ids
}

func seriesIDToScrobbleIDs(seriesID string, extIDs map[string]string) ScrobbleIDs {
	ids := externalIDsToScrobbleIDs(extIDs)

	// Fall back to parsing seriesID if no IDs found.
	// Accept both tmdb:tv:123 / tmdb:series:123 (3+ parts) and tmdb:123 (2 parts).
	if ids.TMDB == 0 && ids.IMDB == "" && seriesID != "" {
		parts := splitSeriesID(seriesID)
		if len(parts) >= 2 {
			provider := strings.ToLower(parts[0])
			numericID := parts[len(parts)-1]
			switch provider {
			case "tmdb":
				ids.TMDB, _ = strconv.Atoi(numericID)
			case "imdb":
				ids.IMDB = "tt" + strings.TrimPrefix(numericID, "tt")
				// Note: TVDB not supported by MDBList — skip
			}
		}
	}

	return ids
}

func splitSeriesID(s string) []string {
	var parts []string
	start := 0
	for i := range s {
		if s[i] == ':' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
