package filter

import (
	"regexp"
	"strings"
)

// Later anime seasons and cours are often released without a season number,
// naming the installment in the title instead ("Shingeki no Kyojin The Final
// Season Part 2 - 01", "Dr Stone New World - 01"). The title parser reads
// "Season N", "Nth Season" and "SN" as seasons but not these names, so such a
// release would otherwise be taken as absolute-numbered.
var (
	animeFinalSeasonPattern = regexp.MustCompile(`\bfinal season\b`)
	animeLaterPartPattern   = regexp.MustCompile(`\b(?:(?:part|cour) (?:[2-9]|[1-9]\d|ii|iii|iv|v|vi)|(?:2nd|3rd|[4-9]th) (?:part|cour))\b`)
	// Metadata season names that only number the season cannot identify it.
	animeGenericSeasonNamePattern = regexp.MustCompile(`^(?:(?:season|series|part|cour|volume|vol|book|chapter|specials?) ?\d*|\d+(?:st|nd|rd|th)? season|\d+)$`)
)

// animeFirstCourMaxEpisode is the shortest common length of a cour. A later
// part that continues its numbering starts after it, so a later part whose
// episodes all fall within it restarted its numbering.
const animeFirstCourMaxEpisode = 12

// animeInstallment is the season a season-less anime release names.
type animeInstallment struct {
	season int    // 0 when the release names a later installment of unknown season
	marker string // the title text naming the installment
	// laterPart marks a later part or cour that restarts its episode numbering,
	// so its episodes are offset by an unknown amount within the season.
	laterPart bool
}

// animeReleaseInstallment places a season-less anime release by the
// installment its title names: a metadata season name, "Final Season" (the
// last season), or a later part or cour. Names inside the expected series
// title itself are ignored.
func animeReleaseInstallment(parsedTitle, expectedTitle string, episodes []int, resolver EpisodeCountResolver) (animeInstallment, bool) {
	title := normalizeForContainment(parsedTitle)
	expected := normalizeForContainment(expectedTitle)
	if title == "" {
		return animeInstallment{}, false
	}
	restarted := len(episodes) > 0
	for _, episode := range episodes {
		if episode > animeFirstCourMaxEpisode {
			restarted = false
		}
	}
	laterPart := ""
	if restarted && !animeLaterPartPattern.MatchString(expected) {
		laterPart = animeLaterPartPattern.FindString(title)
	}

	series, _ := resolver.(*SeriesEpisodeResolver)
	if series != nil {
		best, bestName := 0, ""
		for season, name := range series.SeasonNames {
			name = strings.TrimPrefix(normalizeForContainment(name), "the ")
			if season <= 0 || len(name) < 4 || animeGenericSeasonNamePattern.MatchString(name) ||
				containsWords(expected, name) || !containsWords(title, name) {
				continue
			}
			if len(name) > len(bestName) || (len(name) == len(bestName) && season < best) {
				best, bestName = season, name
			}
		}
		if best > 0 {
			return animeInstallment{season: best, marker: bestName, laterPart: laterPart != ""}, true
		}
	}
	if marker := animeFinalSeasonPattern.FindString(title); marker != "" && !animeFinalSeasonPattern.MatchString(expected) {
		last := 0
		if series != nil {
			for season, count := range series.SeasonEpisodeCounts {
				if count > 0 && season > last {
					last = season
				}
			}
		}
		return animeInstallment{season: last, marker: marker, laterPart: laterPart != ""}, true
	}
	if laterPart != "" {
		return animeInstallment{marker: laterPart, laterPart: true}, true
	}
	return animeInstallment{}, false
}

// containsWords reports whether phrase occurs in text on word boundaries; both
// are normalized with normalizeForContainment.
func containsWords(text, phrase string) bool {
	return phrase != "" && strings.Contains(" "+text+" ", " "+phrase+" ")
}
