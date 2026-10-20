package filter

import (
	"testing"

	"novastream/models"
)

// Attack on Titan as TMDB numbers it: the final season is season 4 and its
// later parts restart at 01 in most fansub releases.
func attackOnTitanResolver(names bool) *SeriesEpisodeResolver {
	resolver := NewSeriesEpisodeResolver(map[int]int{1: 25, 2: 12, 3: 22, 4: 30})
	if names {
		resolver.WithSeasonNames(map[int]string{1: "Season 1", 2: "Season 2", 3: "Season 3", 4: "The Final Season"})
	}
	return resolver
}

func animeTarget(title string, alternates []string, resolver EpisodeCountResolver, season, episode, absolute int) Options {
	return Options{
		ExpectedTitle:         title,
		AlternateTitles:       alternates,
		EpisodeResolver:       resolver,
		IsAnime:               true,
		TargetSeason:          season,
		TargetEpisode:         episode,
		TargetAbsoluteEpisode: absolute,
	}
}

func assertAnimeResults(t *testing.T, tests []struct {
	title string
	opts  Options
	pass  bool
}) {
	t.Helper()
	for _, tt := range tests {
		got := ResultsWithDetails([]models.NZBResult{{Title: tt.title, SizeBytes: 1 << 30}}, tt.opts)
		if len(got) != 1 || got[0].Passed != tt.pass {
			reason := ""
			if len(got) == 1 {
				reason = got[0].RejectReason
			}
			t.Errorf("%q (target S%02dE%02d): passed=%v, want %v (reason %q)",
				tt.title, tt.opts.TargetSeason, tt.opts.TargetEpisode, !tt.pass, tt.pass, reason)
		}
	}
}

var attackOnTitanAlternates = []string{
	"Shingeki no Kyojin",
	"Shingeki no Kyojin The Final Season",
	"Attack on Titan: The Final Season THE FINAL CHAPTERS",
}

// Later seasons released without a season number name the installment in the
// title and restart at 01; they must not satisfy a season 1 absolute target.
func TestAnimeLaterInstallmentDoesNotMatchSeasonOne(t *testing.T) {
	for _, resolver := range []EpisodeCountResolver{nil, attackOnTitanResolver(false), attackOnTitanResolver(true)} {
		aot := animeTarget("Attack on Titan", attackOnTitanAlternates, resolver, 1, 1, 1)
		other := func(title string, episode int) Options { return animeTarget(title, nil, nil, 1, episode, episode) }
		assertAnimeResults(t, []struct {
			title string
			opts  Options
			pass  bool
		}{
			{"[anime-lover]Shingeki no Kyojin The Final Season Part 2 - 01 (Dual Audio 10bit 1080p x265).mkv", aot, false},
			{"Shingeki no Kyojin  The Final Season Part 2 - E01 [1080p] (Attack on Titan  The Final Season Part 2)", aot, false},
			{"[SubsPlease] Shingeki no Kyojin - The Final Season Part 3 - 01 (1080p) [16A521B0]", aot, false},
			{"[Neo-raws] Shingeki no Kyojin - The Final Season - 01 [2160p][Multiple Subtitle].mkv", aot, false},
			{"[TatakaeSubs] Attack on Titan - S01E01 (BD 1080p HEVC OPUS)", aot, true},
			{"[SubsPlease] Shingeki no Kyojin - 01 (1080p)", aot, true},
			// The title parser already places these in a later season.
			{"[SubsPlease] Jujutsu Kaisen 2nd Season - 01 (1080p)", other("Jujutsu Kaisen", 1), false},
			{"[SubsPlease] Mob Psycho 100 III - 01 (1080p)", other("Mob Psycho 100", 1), false},
			{"[SubsPlease] Mushoku Tensei 2nd Cour - 01 (1080p)", other("Mushoku Tensei", 1), false},
			// A later part that continues the series numbering is still season 1.
			{"[SubsPlease] Spy x Family Part 2 - 13 (1080p)", other("Spy x Family", 13), true},
		})
	}
	// Absolute numbering of long-running series is unchanged.
	assertAnimeResults(t, []struct {
		title string
		opts  Options
		pass  bool
	}{
		{"[SubsPlease] One Piece - 1153 (1080p)", animeTarget("One Piece", nil, NewSeriesEpisodeResolver(onePieceSeasonCounts), 22, 95, 1153), true},
	})
}

// A release naming the final season is placed in the last season, numbered
// either within it or from the series start.
func TestAnimeFinalSeasonMatchesLastSeason(t *testing.T) {
	for _, resolver := range []*SeriesEpisodeResolver{attackOnTitanResolver(false), attackOnTitanResolver(true)} {
		s4e5 := animeTarget("Attack on Titan", attackOnTitanAlternates, resolver, 4, 5, 64)
		s4e17 := animeTarget("Attack on Titan", attackOnTitanAlternates, resolver, 4, 17, 76)
		s3e5 := animeTarget("Attack on Titan", attackOnTitanAlternates, resolver, 3, 5, 42)
		assertAnimeResults(t, []struct {
			title string
			opts  Options
			pass  bool
		}{
			{"[Neo-raws] Shingeki no Kyojin - The Final Season - 05 [2160p].mkv", s4e5, true},
			{"[Neo-raws] Shingeki no Kyojin - The Final Season - 06 [2160p].mkv", s4e5, false},
			{"[SubsPlease] Shingeki no Kyojin (The Final Season) - 64 (1080p)", s4e5, true},
			{"[Yameii] Shingeki no Kyojin - The Final Season - 17 [1080p]", s4e17, true},
			{"[SubsPlease] Shingeki no Kyojin (The Final Season) - 76 (1080p)", s4e17, true},
			// Part 2 restarts at 01; where it starts in season 4 is unknown.
			{"[anime-lover]Shingeki no Kyojin The Final Season Part 2 - 01 (1080p)", s4e17, false},
			{"[anime-lover]Shingeki no Kyojin The Final Season Part 2 - 01 (1080p)", animeTarget("Attack on Titan", attackOnTitanAlternates, resolver, 4, 1, 60), false},
			{"[Neo-raws] Shingeki no Kyojin - The Final Season - 05 [2160p].mkv", s3e5, false},
			{"[Judas] Shingeki no Kyojin - The Final Season [Batch][1080p]", s4e5, true},
			{"[Judas] Shingeki no Kyojin - The Final Season [Batch][1080p]", s3e5, false},
		})
	}
}

// Arc-named seasons are placed by the metadata season name.
func TestAnimeSeasonNameFromMetadata(t *testing.T) {
	drStone := NewSeriesEpisodeResolver(drStoneSeasonCounts).WithSeasonNames(map[int]string{
		1: "Stone World", 2: "Stone Wars", 3: "New World", 4: "Science Future",
	})
	demonSlayer := NewSeriesEpisodeResolver(map[int]int{1: 26, 2: 18, 3: 11, 4: 8}).WithSeasonNames(map[int]string{
		1: "Tanjiro Kamado, Unwavering Resolve Arc", 2: "Entertainment District Arc", 3: "Swordsmith Village Arc", 4: "Hashira Training Arc",
	})
	assertAnimeResults(t, []struct {
		title string
		opts  Options
		pass  bool
	}{
		{"[SubsPlease] Dr. Stone - New World - 01 (1080p)", animeTarget("Dr. Stone", nil, drStone, 1, 1, 1), false},
		{"[SubsPlease] Dr. Stone - New World - 01 (1080p)", animeTarget("Dr. Stone", nil, drStone, 3, 1, 36), true},
		{"[SubsPlease] Dr. Stone - New World - 02 (1080p)", animeTarget("Dr. Stone", nil, drStone, 3, 1, 36), false},
		{"[SubsPlease] Dr. Stone - 01 (1080p)", animeTarget("Dr. Stone", nil, drStone, 1, 1, 1), true},
		{"[SubsPlease] Kimetsu no Yaiba - Entertainment District Arc - 03 (1080p)", animeTarget("Demon Slayer: Kimetsu no Yaiba", []string{"Kimetsu no Yaiba"}, demonSlayer, 2, 3, 29), true},
		{"[SubsPlease] Kimetsu no Yaiba - Entertainment District Arc - 03 (1080p)", animeTarget("Demon Slayer: Kimetsu no Yaiba", []string{"Kimetsu no Yaiba"}, demonSlayer, 3, 3, 47), false},
		{"[SubsPlease] Kimetsu no Yaiba - Entertainment District Arc - 03 (1080p)", animeTarget("Demon Slayer: Kimetsu no Yaiba", []string{"Kimetsu no Yaiba"}, demonSlayer, 1, 3, 3), false},
	})
}

func TestAnimeReleaseInstallmentIgnoresSeriesTitle(t *testing.T) {
	if got, ok := animeReleaseInstallment("Kingdom The Final Season", "Kingdom The Final Season", []int{1}, nil); ok {
		t.Fatalf("installment %+v from the expected title itself", got)
	}
	named := NewSeriesEpisodeResolver(map[int]int{1: 12, 2: 12}).WithSeasonNames(map[int]string{1: "Season 1", 2: "2nd Season"})
	if got, ok := animeReleaseInstallment("Some Show Season 1", "Some Show", []int{1}, named); ok {
		t.Fatalf("generic season name matched: %+v", got)
	}
}
