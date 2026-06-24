package history

import (
	"testing"

	"novastream/models"
)

func TestSeriesTVDBIDFromUpdate(t *testing.T) {
	cases := []struct {
		name   string
		update models.PlaybackProgressUpdate
		want   int
	}{
		{
			name:   "from external ids",
			update: models.PlaybackProgressUpdate{ExternalIDs: map[string]string{"tvdb": "75805"}},
			want:   75805,
		},
		{
			name:   "from tvdb series id",
			update: models.PlaybackProgressUpdate{SeriesID: "tvdb:series:328634"},
			want:   328634,
		},
		{
			name:   "external ids take precedence",
			update: models.PlaybackProgressUpdate{ExternalIDs: map[string]string{"tvdb": "1"}, SeriesID: "tvdb:series:2"},
			want:   1,
		},
		{
			name:   "non-tvdb series id ignored",
			update: models.PlaybackProgressUpdate{SeriesID: "tmdb:tv:999"},
			want:   0,
		},
		{
			name:   "no ids",
			update: models.PlaybackProgressUpdate{},
			want:   0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := seriesTVDBIDFromUpdate(tc.update); got != tc.want {
				t.Fatalf("seriesTVDBIDFromUpdate = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSeriesOrderingIsAlternate verifies the scrobble gate predicate. Without a
// store configured it must never report alternate (fails open to sync-safe).
func TestSeriesOrderingIsAlternateNoStore(t *testing.T) {
	s := &Service{}
	if s.seriesOrderingIsAlternate("user", 75805) {
		t.Fatal("expected no alternate ordering without a store")
	}
	if got, err := s.GetSeriesOrdering("user", 75805); err != nil || got != "" {
		t.Fatalf("GetSeriesOrdering without store = (%q, %v), want (\"\", nil)", got, err)
	}
}
