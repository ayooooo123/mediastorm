package filter

import (
	"regexp"
	"strconv"
	"strings"
)

// ptt-go reads "E01-E12" and "EP1156-1170" as ranges, but keeps only the first
// episode of "E1086-1155" and "E01~12". Recover the range from the raw title.
var episodeRangePattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ep?[ .]?(\d{1,4})[ .]*[-~][ .]*(?:ep?[ .]?)?(\d{1,4})(?:[^0-9a-z]|$)`)

// maxRecoveredEpisodeSpan bounds a recovered range so a resolution, year or
// other number after the dash is not read as its end.
const maxRecoveredEpisodeSpan = 300

// recoverEpisodeRange returns the episode range that rawTitle spells out when
// the parser kept only its first episode.
func recoverEpisodeRange(rawTitle string, episodes []int) ([]int, bool) {
	if len(episodes) != 1 {
		return nil, false
	}
	for _, match := range episodeRangePattern.FindAllStringSubmatch(rawTitle, -1) {
		first, err1 := strconv.Atoi(match[1])
		last, err2 := strconv.Atoi(match[2])
		if err1 != nil || err2 != nil || first != episodes[0] || last <= first || last-first > maxRecoveredEpisodeSpan {
			continue
		}
		recovered := make([]int, 0, last-first+1)
		for episode := first; episode <= last; episode++ {
			recovered = append(recovered, episode)
		}
		return recovered, true
	}
	return nil, false
}

// Manga, light novels and other reading material are indexed alongside video
// under the series name ("One Piece 1-890 Original Manga").
var nonVideoMarkers = []string{"cbz", "cbr", "epub", "mobi", "azw3", "pdf", "scans", "scanlation", "artbook"}

// hasNonVideoMarker reports whether the title text after the matched series
// title identifies reading material rather than a video release.
func hasNonVideoMarker(rawTitle, matchedTitle string) bool {
	suffix, ok := normalizedTitleSuffix(rawTitle, matchedTitle)
	if !ok || suffix == "" {
		return false
	}
	fields := strings.Fields(suffix)
	for i, field := range fields {
		for _, marker := range nonVideoMarkers {
			if field == marker {
				return true
			}
		}
		if field == "manga" {
			// Manga Entertainment / Manga UK release anime on video.
			next := ""
			if i+1 < len(fields) {
				next = fields[i+1]
			}
			if next != "entertainment" && next != "uk" && next != "video" && next != "films" {
				return true
			}
		}
	}
	padded := " " + suffix + " "
	return strings.Contains(padded, " light novel ") || strings.Contains(padded, " art book ")
}
