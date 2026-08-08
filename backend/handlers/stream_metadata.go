package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// StreamMediaMetadata carries canonical media identity alongside an active stream/session.
// This lets dashboards render exact titles and progress without reparsing filenames.
type StreamMediaMetadata struct {
	SourceServiceType    string
	MediaType            string
	ItemID               string
	Title                string
	Year                 int
	SeasonNumber         int
	EpisodeNumber        int
	EpisodeName          string
	SeriesID             string
	SeriesName           string
	MovieName            string
	PosterURL            string
	NotificationImageURL string
	LiveSourceURL        string
	LiveSourceID         string
	LiveChannelLogo      string
	ExternalIDs          map[string]string
}

func parseStreamMediaMetadata(r *http.Request) StreamMediaMetadata {
	q := r.URL.Query()
	meta := StreamMediaMetadata{
		SourceServiceType:    normalizeStreamSourceServiceType(q.Get("sourceServiceType")),
		MediaType:            strings.ToLower(strings.TrimSpace(q.Get("mediaType"))),
		ItemID:               strings.ToLower(strings.TrimSpace(q.Get("itemId"))),
		Title:                strings.TrimSpace(q.Get("title")),
		EpisodeName:          strings.TrimSpace(q.Get("episodeName")),
		SeriesID:             strings.TrimSpace(q.Get("seriesId")),
		SeriesName:           strings.TrimSpace(q.Get("seriesName")),
		MovieName:            strings.TrimSpace(q.Get("movieName")),
		PosterURL:            strings.TrimSpace(q.Get("posterUrl")),
		NotificationImageURL: strings.TrimSpace(q.Get("notificationImageUrl")),
		LiveSourceURL:        strings.TrimSpace(q.Get("liveSourceUrl")),
		LiveSourceID:         strings.TrimSpace(q.Get("liveSourceId")),
		LiveChannelLogo:      strings.TrimSpace(q.Get("liveChannelLogo")),
	}

	if meta.Title == "" {
		if meta.MediaType == "episode" {
			meta.Title = meta.SeriesName
		} else {
			meta.Title = meta.MovieName
		}
	}

	if n, ok := parseOptionalInt(q.Get("year")); ok {
		meta.Year = n
	}
	if n, ok := parseOptionalInt(q.Get("seasonNumber")); ok {
		meta.SeasonNumber = n
	}
	if n, ok := parseOptionalInt(q.Get("episodeNumber")); ok {
		meta.EpisodeNumber = n
	}

	if ids := parseExternalIDs(q); len(ids) > 0 {
		meta.ExternalIDs = ids
	}

	return meta
}

func addStreamMediaMetadataParams(values url.Values, meta StreamMediaMetadata) {
	if serviceType := normalizeStreamSourceServiceType(meta.SourceServiceType); serviceType != "" {
		values.Set("sourceServiceType", serviceType)
	}
	if meta.MediaType != "" {
		values.Set("mediaType", meta.MediaType)
	}
	if meta.ItemID != "" {
		values.Set("itemId", meta.ItemID)
	}
	if meta.Title != "" {
		values.Set("title", meta.Title)
	}
	if meta.EpisodeName != "" {
		values.Set("episodeName", meta.EpisodeName)
	}
	if meta.SeriesID != "" {
		values.Set("seriesId", meta.SeriesID)
	}
	if meta.SeriesName != "" {
		values.Set("seriesName", meta.SeriesName)
	}
	if meta.MovieName != "" {
		values.Set("movieName", meta.MovieName)
	}
	if meta.PosterURL != "" {
		values.Set("posterUrl", meta.PosterURL)
	}
	if meta.NotificationImageURL != "" {
		values.Set("notificationImageUrl", meta.NotificationImageURL)
	}
	if meta.LiveSourceURL != "" {
		values.Set("liveSourceUrl", meta.LiveSourceURL)
	}
	if meta.LiveSourceID != "" {
		values.Set("liveSourceId", meta.LiveSourceID)
	}
	if meta.LiveChannelLogo != "" {
		values.Set("liveChannelLogo", meta.LiveChannelLogo)
	}
	if meta.Year > 0 {
		values.Set("year", strconv.Itoa(meta.Year))
	}
	if meta.SeasonNumber > 0 {
		values.Set("seasonNumber", strconv.Itoa(meta.SeasonNumber))
	}
	if meta.EpisodeNumber > 0 {
		values.Set("episodeNumber", strconv.Itoa(meta.EpisodeNumber))
	}
	for key, value := range meta.ExternalIDs {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
}

func normalizeStreamSourceServiceType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debrid", "usenet", "local", "plex", "jellyfin":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func parseOptionalInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseExternalIDs(values url.Values) map[string]string {
	ids := make(map[string]string)
	for _, key := range []string{"imdb", "tmdb", "tvdb", "titleId"} {
		value := strings.TrimSpace(values.Get(key))
		if value != "" {
			ids[key] = value
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}
