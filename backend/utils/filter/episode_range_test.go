package filter

import (
	"slices"
	"testing"

	"novastream/models"
)

func TestRecoverEpisodeRange(t *testing.T) {
	tests := []struct {
		title     string
		parsed    []int
		wantFirst int
		wantLast  int
	}{
		{"One.Piece.Arc.Egghead.E1086-1155.VOSTFR.1080p.WEBRiP.x265-KAF", []int{1086}, 1086, 1155},
		{"One Piece E01~12 1080p", []int{1}, 1, 12},
		{"Show EP 05 - 08 720p", []int{5}, 5, 8},
		// Not ranges: a resolution after the dash, a backwards range, or a span
		// too long to be one release.
		{"One Piece E12-2160p", []int{12}, 0, 0},
		{"Show E20-12 1080p", []int{20}, 0, 0},
		{"Show E01-2024 1080p", []int{1}, 0, 0},
		// The parser already returned a range.
		{"One Piece E01-E12 1080p", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, 0, 0},
	}
	for _, tt := range tests {
		got, ok := recoverEpisodeRange(tt.title, tt.parsed)
		if tt.wantLast == 0 {
			if ok {
				t.Errorf("%q: recovered %d-%d, want no range", tt.title, got[0], got[len(got)-1])
			}
			continue
		}
		if !ok || got[0] != tt.wantFirst || got[len(got)-1] != tt.wantLast || len(got) != tt.wantLast-tt.wantFirst+1 {
			t.Errorf("%q: got %v (ok=%v), want %d-%d", tt.title, got, ok, tt.wantFirst, tt.wantLast)
		}
	}
}

// "E1086-1155" was read as episode 1086 alone, rejecting the arc batch for
// every other episode it contains.
func TestEpisodeRangeBatchMatchesContainedEpisode(t *testing.T) {
	resolver := NewSeriesEpisodeResolver(map[int]int{21: 197, 22: 67}).WithSeasonNames(map[int]string{21: "Wano Country Arc", 22: "Egghead"})
	for _, absolute := range []int{1086, 1120, 1155} {
		got := ResultsWithDetails([]models.NZBResult{{Title: "One.Piece.Arc.Egghead.E1086-1155.VOSTFR.1080p.WEBRiP.x265-KAF", SizeBytes: 1 << 30}}, Options{
			ExpectedTitle: "One Piece", IsAnime: true, EpisodeResolver: resolver,
			TargetSeason: 22, TargetEpisode: absolute - 1088, TargetAbsoluteEpisode: absolute,
		})
		if !got[0].Passed {
			t.Errorf("absolute %d: rejected (%s)", absolute, got[0].RejectReason)
		}
	}
	got := ResultsWithDetails([]models.NZBResult{{Title: "One.Piece.Arc.Egghead.E1086-1155.VOSTFR.1080p.WEBRiP.x265-KAF", SizeBytes: 1 << 30}}, Options{
		ExpectedTitle: "One Piece", IsAnime: true, EpisodeResolver: resolver,
		TargetSeason: 22, TargetEpisode: 70, TargetAbsoluteEpisode: 1158,
	})
	if got[0].Passed {
		t.Error("absolute 1158 outside the batch passed")
	}
}

func TestNonVideoReleasesRejected(t *testing.T) {
	series := Options{ExpectedTitle: "One Piece", IsAnime: true, TargetSeason: 13, TargetEpisode: 10, TargetAbsoluteEpisode: 431}
	movie := Options{ExpectedTitle: "Akira", ExpectedYear: 1988, IsMovie: true}
	tests := []struct {
		title string
		opts  Options
		pass  bool
	}{
		{"One Piece 1-890 Original Manga", series, false},
		{"One Piece Volume 1-105 Manga CBZ", series, false},
		{"One Piece Light Novel Vol 1-3 EPUB", series, false},
		{"Akira 1988 Manga Scans PDF", movie, false},
		{"One Piece 001-600 [1080p JAP - ITA / IT - EN - ES - PT/BR]", series, true},
		// Manga Entertainment distributes anime on video.
		{"Akira 1988 1080p BluRay Manga Entertainment Dub x264", movie, true},
	}
	var failed []string
	for _, tt := range tests {
		got := ResultsWithDetails([]models.NZBResult{{Title: tt.title, SizeBytes: 1 << 30}}, tt.opts)
		if got[0].Passed != tt.pass {
			failed = append(failed, tt.title+" ("+got[0].RejectReason+")")
		}
	}
	if !slices.Equal(failed, nil) {
		t.Errorf("wrong result for: %q", failed)
	}
}
