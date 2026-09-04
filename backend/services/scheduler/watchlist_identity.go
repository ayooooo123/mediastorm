package scheduler

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"novastream/models"
)

// enrichSyncedWatchlistIdentity resolves canonical provider IDs when a sync
// source supplies only its own identifier. Without this step, the same title
// can be imported beside an existing TMDB item and metadata hydration has no
// stable identity to work from.
func (s *Service) enrichSyncedWatchlistIdentity(source string, input models.WatchlistUpsert) models.WatchlistUpsert {
	input.ExternalIDs = schedulerNormalizeExternalIDs(input.ExternalIDs)
	if input.ExternalIDs["tmdb"] != "" {
		return input
	}

	s.mu.RLock()
	meta := s.metadataService
	s.mu.RUnlock()
	if meta == nil || strings.TrimSpace(input.Name) == "" {
		return input
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var (
		title *models.Title
		err   error
	)
	mediaType := strings.ToLower(strings.TrimSpace(input.MediaType))
	switch mediaType {
	case "series", "show", "tv":
		details, detailsErr := meta.SeriesDetailsLite(ctx, models.SeriesDetailsQuery{
			TitleID: input.ID,
			Name:    input.Name,
			Year:    input.Year,
			TVDBID:  syncedWatchlistNumericID(input.ExternalIDs, "tvdb"),
			TMDBID:  syncedWatchlistNumericID(input.ExternalIDs, "tmdb"),
			IMDBID:  input.ExternalIDs["imdb"],
		})
		err = detailsErr
		if details != nil {
			title = &details.Title
		}
	case "movie":
		title, err = meta.MovieDetails(ctx, models.MovieDetailsQuery{
			TitleID: input.ID,
			Name:    input.Name,
			Year:    input.Year,
			TVDBID:  syncedWatchlistNumericID(input.ExternalIDs, "tvdb"),
			TMDBID:  syncedWatchlistNumericID(input.ExternalIDs, "tmdb"),
			IMDBID:  input.ExternalIDs["imdb"],
		})
	default:
		return input
	}
	if err != nil || title == nil {
		if err != nil {
			log.Printf("[scheduler] %s watchlist identity enrichment failed title=%q year=%d: %v", source, input.Name, input.Year, err)
		}
		return input
	}

	if input.ExternalIDs == nil {
		input.ExternalIDs = make(map[string]string)
	}
	if title.TMDBID > 0 {
		input.ExternalIDs["tmdb"] = strconv.FormatInt(title.TMDBID, 10)
	}
	if title.TVDBID > 0 {
		input.ExternalIDs["tvdb"] = strconv.FormatInt(title.TVDBID, 10)
	}
	if strings.TrimSpace(title.IMDBID) != "" {
		input.ExternalIDs["imdb"] = strings.TrimSpace(title.IMDBID)
	}
	if strings.TrimSpace(title.Name) != "" {
		input.Name = strings.TrimSpace(title.Name)
	}
	if input.Year == 0 {
		input.Year = title.Year
	}
	if input.Overview == "" {
		input.Overview = title.Overview
	}
	if input.PosterURL == "" && title.Poster != nil {
		input.PosterURL = title.Poster.URL
	}
	if input.BackdropURL == "" && title.Backdrop != nil {
		input.BackdropURL = title.Backdrop.URL
	}
	if input.RuntimeMinutes == 0 {
		input.RuntimeMinutes = title.RuntimeMinutes
	}
	if len(input.Genres) == 0 && len(title.Genres) > 0 {
		input.Genres = append([]string(nil), title.Genres...)
	}
	if input.Status == "" {
		input.Status = title.Status
	}
	if input.LifecycleStatus == "" {
		input.LifecycleStatus = title.LifecycleStatus
	}
	return input
}

func syncedWatchlistNumericID(ids map[string]string, key string) int64 {
	value := strings.TrimSpace(ids[key])
	if value == "" {
		return 0
	}
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}

func preferredWatchlistSourceID(ids map[string]string) string {
	for _, key := range []string{"tmdb", "imdb", "tvdb", "trakt", "plex", "jellyfin"} {
		if value := strings.TrimSpace(ids[key]); value != "" {
			return value
		}
	}
	return ""
}
