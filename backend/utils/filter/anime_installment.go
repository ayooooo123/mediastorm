package filter

import "regexp"

// Later anime seasons and cours are often released without a season number,
// naming the installment in the title instead and restarting the episode count
// ("Shingeki no Kyojin The Final Season Part 2 - 01"). The title parser reads
// "Season N", "Nth Season" and "SN" as seasons but not these markers, so such a
// release would otherwise be taken as absolute-numbered and match season 1.
var (
	animeFinalSeasonPattern = regexp.MustCompile(`\bfinal season\b`)
	animeLaterPartPattern   = regexp.MustCompile(`\b(?:(?:part|cour) (?:[2-9]|[1-9]\d|ii|iii|iv|v|vi)|(?:2nd|3rd|[4-9]th) (?:part|cour))\b`)
)

// animeFirstCourMaxEpisode is the shortest common length of a cour. A later
// part that continues the series numbering starts after it, so a later part
// whose episodes all fall within it restarted its numbering.
const animeFirstCourMaxEpisode = 12

// laterAnimeInstallmentMarker reports the marker that places a season-less
// anime release after season 1, or "" when it may belong to season 1. Markers
// that are part of the expected series title itself are ignored.
func laterAnimeInstallmentMarker(parsedTitle, expectedTitle string, episodes []int) string {
	title := normalizeForContainment(parsedTitle)
	expected := normalizeForContainment(expectedTitle)
	if marker := animeFinalSeasonPattern.FindString(title); marker != "" && !animeFinalSeasonPattern.MatchString(expected) {
		return marker
	}
	marker := animeLaterPartPattern.FindString(title)
	if marker == "" || animeLaterPartPattern.MatchString(expected) || len(episodes) == 0 {
		return ""
	}
	// "Part 2 - 13" continuing the series numbering is a valid season 1 episode
	// for series whose first season spans several parts.
	for _, episode := range episodes {
		if episode > animeFirstCourMaxEpisode {
			return ""
		}
	}
	return marker
}
