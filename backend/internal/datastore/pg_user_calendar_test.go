package datastore

import (
	"reflect"
	"testing"

	"novastream/models"
)

func TestUserCalendarItemsRoundTrip(t *testing.T) {
	cases := [][]models.CalendarItem{
		nil,
		{},
		{
			{Title: "Show", MediaType: "series", SeasonNumber: 2, EpisodeNumber: 3, AirDate: "2026-10-20", AirTime: "21:00", AirTimezone: "America/New_York", ExternalIDs: map[string]string{"tvdb": "1"}, Source: "watchlist"},
			{Title: "Film", MediaType: "movie", AirDate: "2026-11-01", ReleaseType: "digital", Source: "trending", SourceRank: 4},
		},
	}
	for _, items := range cases {
		body, err := encodeUserCalendarItems(items)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeUserCalendarItems(body)
		if err != nil {
			t.Fatal(err)
		}
		want := items
		if want == nil {
			want = []models.CalendarItem{}
		}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("decoded %+v, want %+v", decoded, want)
		}
	}
}

func TestUserCalendarItemsRejectsGarbage(t *testing.T) {
	if _, err := decodeUserCalendarItems([]byte("not gzip")); err == nil {
		t.Fatal("expected error for non-gzip body")
	}
}
