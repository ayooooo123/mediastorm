package handlers

import (
	"net/http"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

type unreleasedVisibilityScope string

const (
	unreleasedVisibilityLists  unreleasedVisibilityScope = "lists"
	unreleasedVisibilitySearch unreleasedVisibilityScope = "search"
)

type unreleasedVisibilityPolicy struct {
	IncludeMovies bool
	IncludeShows  bool
}

func requestClientID(r *http.Request) string {
	if r == nil {
		return ""
	}
	clientID := strings.TrimSpace(r.Header.Get("X-Client-ID"))
	if clientID == "" {
		clientID = strings.TrimSpace(r.URL.Query().Get("clientId"))
	}
	return clientID
}

func resolveUnreleasedVisibilityPolicy(
	cfg *config.Manager,
	userSettings userSettingsProvider,
	clientSettings clientSettingsService,
	userID string,
	clientID string,
	scope unreleasedVisibilityScope,
) unreleasedVisibilityPolicy {
	policy := unreleasedVisibilityPolicy{IncludeMovies: true, IncludeShows: true}
	if cfg != nil {
		if settings, err := cfg.Load(); err == nil && displayUnreleasedVisibilityInitialized(settings.Display) {
			switch scope {
			case unreleasedVisibilitySearch:
				policy.IncludeMovies = settings.Display.IncludeUnreleasedMoviesInSearch
				policy.IncludeShows = settings.Display.IncludeUnreleasedShowsInSearch
			default:
				policy.IncludeMovies = settings.Display.IncludeUnreleasedMoviesInLists
				policy.IncludeShows = settings.Display.IncludeUnreleasedShowsInLists
			}
		}
	}
	if userSettings != nil && strings.TrimSpace(userID) != "" {
		if profileSettings, err := userSettings.Get(userID); err == nil && profileSettings != nil {
			applyDisplayUnreleasedVisibility(&policy, profileSettings.Display, scope)
		}
	}
	if clientSettings != nil && strings.TrimSpace(clientID) != "" {
		if cs, err := clientSettings.Get(clientID); err == nil && cs != nil {
			applyClientUnreleasedVisibility(&policy, cs, scope)
		}
	}
	return policy
}

func displayUnreleasedVisibilityInitialized(display config.DisplaySettings) bool {
	if display.IncludeUnreleasedMoviesInLists ||
		display.IncludeUnreleasedShowsInLists ||
		display.IncludeUnreleasedMoviesInSearch ||
		display.IncludeUnreleasedShowsInSearch {
		return true
	}

	return len(display.BadgeVisibility) > 0 ||
		len(display.NavigationTabVisibility) > 0 ||
		strings.TrimSpace(display.WatchStateIconStyle) != "" ||
		display.HideWatched ||
		display.AlwaysShowProfileSelector ||
		display.BypassFilteringForAIOStreamsOnly ||
		display.ShowParsedBadges ||
		display.CleanPosters ||
		display.DisableMobileTopCarousel ||
		display.BlurUnwatchedEpisodeThumbnails ||
		display.BlurUnwatchedEpisodeThumbnailsIncludeCurrent ||
		display.BlurUnwatchedEpisodeOverviews ||
		display.BlurUnwatchedEpisodeOverviewsIncludeCurrent ||
		strings.TrimSpace(display.AppLanguage) != "" ||
		display.Appearance.FontScale != nil ||
		strings.TrimSpace(display.Branding.HomeTVImageURL) != "" ||
		strings.TrimSpace(display.Branding.HomeMobileLogoURL) != "" ||
		strings.TrimSpace(display.Branding.SettingsTVImageURL) != ""
}

func applyDisplayUnreleasedVisibility(policy *unreleasedVisibilityPolicy, display models.DisplaySettings, scope unreleasedVisibilityScope) {
	if policy == nil {
		return
	}
	switch scope {
	case unreleasedVisibilitySearch:
		if display.IncludeUnreleasedMoviesInSearch != nil {
			policy.IncludeMovies = *display.IncludeUnreleasedMoviesInSearch
		}
		if display.IncludeUnreleasedShowsInSearch != nil {
			policy.IncludeShows = *display.IncludeUnreleasedShowsInSearch
		}
	default:
		if display.IncludeUnreleasedMoviesInLists != nil {
			policy.IncludeMovies = *display.IncludeUnreleasedMoviesInLists
		}
		if display.IncludeUnreleasedShowsInLists != nil {
			policy.IncludeShows = *display.IncludeUnreleasedShowsInLists
		}
	}
}

func applyClientUnreleasedVisibility(policy *unreleasedVisibilityPolicy, settings *models.ClientFilterSettings, scope unreleasedVisibilityScope) {
	if policy == nil || settings == nil {
		return
	}
	switch scope {
	case unreleasedVisibilitySearch:
		if settings.IncludeUnreleasedMoviesInSearch != nil {
			policy.IncludeMovies = *settings.IncludeUnreleasedMoviesInSearch
		}
		if settings.IncludeUnreleasedShowsInSearch != nil {
			policy.IncludeShows = *settings.IncludeUnreleasedShowsInSearch
		}
	default:
		if settings.IncludeUnreleasedMoviesInLists != nil {
			policy.IncludeMovies = *settings.IncludeUnreleasedMoviesInLists
		}
		if settings.IncludeUnreleasedShowsInLists != nil {
			policy.IncludeShows = *settings.IncludeUnreleasedShowsInLists
		}
	}
}

func filterSearchResultsByUnreleasedVisibility(results []models.SearchResult, policy unreleasedVisibilityPolicy) []models.SearchResult {
	if policy.IncludeMovies && policy.IncludeShows {
		return results
	}
	filtered := make([]models.SearchResult, 0, len(results))
	for _, result := range results {
		if titleAllowedByUnreleasedVisibility(result.Title, policy) {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func filterTrendingItemsByUnreleasedVisibility(items []models.TrendingItem, policy unreleasedVisibilityPolicy) []models.TrendingItem {
	if policy.IncludeMovies && policy.IncludeShows {
		return items
	}
	filtered := make([]models.TrendingItem, 0, len(items))
	for _, item := range items {
		if titleAllowedByUnreleasedVisibility(item.Title, policy) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterTitlesByUnreleasedVisibility(titles []models.Title, policy unreleasedVisibilityPolicy) []models.Title {
	if policy.IncludeMovies && policy.IncludeShows {
		return titles
	}
	filtered := make([]models.Title, 0, len(titles))
	for _, title := range titles {
		if titleAllowedByUnreleasedVisibility(title, policy) {
			filtered = append(filtered, title)
		}
	}
	return filtered
}

func filterWatchlistItemsByUnreleasedVisibility(items []models.WatchlistItem, policy unreleasedVisibilityPolicy) []models.WatchlistItem {
	if policy.IncludeMovies && policy.IncludeShows {
		return items
	}
	filtered := make([]models.WatchlistItem, 0, len(items))
	for _, item := range items {
		title := models.Title{
			MediaType:   item.MediaType,
			Status:      item.Status,
			Year:        item.Year,
			Theatrical:  item.Theatrical,
			HomeRelease: item.HomeRelease,
		}
		if titleAllowedByUnreleasedVisibility(title, policy) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func titleAllowedByUnreleasedVisibility(title models.Title, policy unreleasedVisibilityPolicy) bool {
	switch strings.ToLower(strings.TrimSpace(title.MediaType)) {
	case "movie":
		return policy.IncludeMovies || titleMovieReleasedForUnreleasedVisibility(title)
	case "series", "show", "tv":
		return policy.IncludeShows || strings.EqualFold(strings.TrimSpace(title.Status), models.SeriesReleaseStatusReleased)
	default:
		return true
	}
}

func titleMovieReleasedForUnreleasedVisibility(title models.Title) bool {
	status := strings.TrimSpace(title.Status)
	if status != "" && !strings.EqualFold(status, models.MovieReleaseStatusUnknown) {
		return strings.EqualFold(status, models.MovieReleaseStatusReleased)
	}
	if models.MovieReleaseStatus(title) == models.MovieReleaseStatusReleased {
		return true
	}
	return title.Year > 0 && title.Year < currentYear()
}

func currentYear() int {
	return time.Now().Year()
}
