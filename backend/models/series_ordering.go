package models

import "time"

// SeriesOrderingPref stores a user's chosen TVDB episode ordering for a single
// series. Absence of a row means the default (official/primary) ordering, which
// is sync-safe. A non-"official" SeasonType disables scrobbling/external sync
// for the series because episode numbering diverges from the canonical order.
type SeriesOrderingPref struct {
	SeriesTVDBID int64     `json:"seriesTvdbId"`
	SeasonType   string    `json:"seasonType"` // lowercase TVDB season-type key
	UpdatedAt    time.Time `json:"updatedAt"`
}
