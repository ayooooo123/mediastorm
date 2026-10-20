package filter

import (
	"testing"

	"novastream/models"
)

// Later seasons released without a season number name the installment in the
// title and restart at 01; they must not satisfy a season 1 absolute target.
func TestAnimeLaterInstallmentDoesNotMatchSeasonOne(t *testing.T) {
	attackOnTitan := Options{
		ExpectedTitle: "Attack on Titan",
		AlternateTitles: []string{
			"Shingeki no Kyojin",
			"Shingeki no Kyojin The Final Season",
			"Attack on Titan: The Final Season THE FINAL CHAPTERS",
		},
		IsAnime:               true,
		TargetSeason:          1,
		TargetEpisode:         1,
		TargetAbsoluteEpisode: 1,
	}
	withTitle := func(opts Options, title string, season, episode, absolute int) Options {
		opts.ExpectedTitle = title
		opts.AlternateTitles = nil
		opts.TargetSeason, opts.TargetEpisode, opts.TargetAbsoluteEpisode = season, episode, absolute
		return opts
	}

	tests := []struct {
		title string
		opts  Options
		pass  bool
	}{
		{"[anime-lover]Shingeki no Kyojin The Final Season Part 2 - 01 (Dual Audio 10bit 1080p x265).mkv", attackOnTitan, false},
		{"Shingeki no Kyojin  The Final Season Part 2 - E01 [1080p] (Attack on Titan  The Final Season Part 2)", attackOnTitan, false},
		{"[SubsPlease] Shingeki no Kyojin - The Final Season Part 3 - 01 (1080p) [16A521B0]", attackOnTitan, false},
		{"[Neo-raws] Shingeki no Kyojin - The Final Season - 01 [2160p][Multiple Subtitle].mkv", attackOnTitan, false},
		{"[TatakaeSubs] Attack on Titan - S01E01 (BD 1080p HEVC OPUS)", attackOnTitan, true},
		{"[SubsPlease] Shingeki no Kyojin - 01 (1080p)", attackOnTitan, true},
		// The title parser already places these in a later season.
		{"[SubsPlease] Jujutsu Kaisen 2nd Season - 01 (1080p)", withTitle(attackOnTitan, "Jujutsu Kaisen", 1, 1, 1), false},
		{"[SubsPlease] Mob Psycho 100 III - 01 (1080p)", withTitle(attackOnTitan, "Mob Psycho 100", 1, 1, 1), false},
		{"[SubsPlease] Mushoku Tensei 2nd Cour - 01 (1080p)", withTitle(attackOnTitan, "Mushoku Tensei", 1, 1, 1), false},
		// A later part that continues the series numbering is still season 1.
		{"[SubsPlease] Spy x Family Part 2 - 13 (1080p)", withTitle(attackOnTitan, "Spy x Family", 1, 13, 13), true},
		// Absolute numbering of long-running series is unchanged.
		{"[SubsPlease] One Piece - 1153 (1080p)", withTitle(attackOnTitan, "One Piece", 22, 95, 1153), true},
	}
	for _, tt := range tests {
		got := ResultsWithDetails([]models.NZBResult{{Title: tt.title, SizeBytes: 1 << 30}}, tt.opts)
		if len(got) != 1 || got[0].Passed != tt.pass {
			reason := ""
			if len(got) == 1 {
				reason = got[0].RejectReason
			}
			t.Errorf("%q: passed=%v, want %v (reason %q)", tt.title, !tt.pass, tt.pass, reason)
		}
	}
}

func TestLaterAnimeInstallmentMarkerIgnoresSeriesTitle(t *testing.T) {
	if marker := laterAnimeInstallmentMarker("Kingdom The Final Season", "Kingdom The Final Season", []int{1}); marker != "" {
		t.Fatalf("marker %q from the expected title itself", marker)
	}
	if marker := laterAnimeInstallmentMarker("Shingeki no Kyojin The Final Season Part 2", "Attack on Titan", []int{1}); marker != "final season" {
		t.Fatalf("got marker %q, want final season", marker)
	}
}
