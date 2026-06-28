package models

import "time"

// HiddenItem is a profile-scoped title suppression marker for home/list shelves.
type HiddenItem struct {
	ID          string            `json:"id"`
	MediaType   string            `json:"mediaType"`
	Name        string            `json:"name,omitempty"`
	Year        int               `json:"year,omitempty"`
	PosterURL   string            `json:"posterUrl,omitempty"`
	BackdropURL string            `json:"backdropUrl,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
	HiddenAt    time.Time         `json:"hiddenAt"`
}

// HiddenItemUpsert captures data required to hide a title.
type HiddenItemUpsert struct {
	ID          string            `json:"id"`
	MediaType   string            `json:"mediaType"`
	Name        string            `json:"name,omitempty"`
	Year        int               `json:"year,omitempty"`
	PosterURL   string            `json:"posterUrl,omitempty"`
	BackdropURL string            `json:"backdropUrl,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
}

// Key returns a stable identifier for the hidden item.
func (h HiddenItem) Key() string {
	return h.MediaType + ":" + h.ID
}
