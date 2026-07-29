package models

import "time"

const (
	NotificationChannelDiscord = "discord"
	NotificationChannelWebhook = "webhook"

	NotificationEventWatchStarted  = "watch.started"
	NotificationEventWatchProgress = "watch.progress"
	NotificationEventWatchResumed  = "watch.resumed"
	NotificationEventWatchWatched  = "watch.watched"
	NotificationEventRelease       = "release.available"
)

// NotificationChannel is a profile-owned destination and its subscriptions.
type NotificationChannel struct {
	ID              string    `json:"id"`
	ProfileID       string    `json:"profileId"`
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	URL             string    `json:"url,omitempty"`
	URLConfigured   bool      `json:"urlConfigured"`
	Enabled         bool      `json:"enabled"`
	Events          []string  `json:"events"`
	NotifyWatchlist bool      `json:"notifyWatchlist"`
	NotifyTrending  bool      `json:"notifyTrending"`
	TrendingLimit   int       `json:"trendingLimit"`
	ReleaseTypes    []string  `json:"releaseTypes"`
	TitleTemplate   string    `json:"titleTemplate"`
	BodyTemplate    string    `json:"bodyTemplate"`
	IncludePoster   bool      `json:"includePoster"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NotificationObservation records the last release state seen for an item.
type NotificationObservation struct {
	ProfileID string
	ItemKey   string
	Status    string
	Event     NotificationEvent
	UpdatedAt time.Time
}

// NotificationProgressMessage stores the Discord message backing an active
// playback progress notification so it can be adopted after a backend restart.
type NotificationProgressMessage struct {
	ChannelID   string    `json:"channelId"`
	ProfileID   string    `json:"profileId"`
	PlaybackKey string    `json:"playbackKey"`
	MessageID   string    `json:"messageId"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// NotificationEvent is the provider-neutral input to notification formatting.
type NotificationEvent struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	ProfileID     string            `json:"profileId"`
	Title         string            `json:"title"`
	MediaType     string            `json:"mediaType"`
	Year          int               `json:"year,omitempty"`
	SeriesTitle   string            `json:"seriesTitle,omitempty"`
	EpisodeTitle  string            `json:"episodeTitle,omitempty"`
	SeasonNumber  int               `json:"seasonNumber,omitempty"`
	EpisodeNumber int               `json:"episodeNumber,omitempty"`
	Position      float64           `json:"position,omitempty"`
	Duration      float64           `json:"duration,omitempty"`
	Percent       float64           `json:"percent,omitempty"`
	ReleaseType   string            `json:"releaseType,omitempty"`
	ReleaseDate   string            `json:"releaseDate,omitempty"`
	Source        string            `json:"source,omitempty"`
	SourceRank    int               `json:"sourceRank,omitempty"`
	PosterURL     string            `json:"posterUrl,omitempty"`
	ExternalIDs   map[string]string `json:"externalIds,omitempty"`
	OccurredAt    time.Time         `json:"occurredAt"`
}
