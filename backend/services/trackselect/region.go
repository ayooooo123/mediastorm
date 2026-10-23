package trackselect

import (
	"strings"

	"novastream/models"
)

// regionalVariant is one regional dub of a language (Québec French, Latin
// American Spanish, ...). Releases rarely tag these in the container language
// (both French dubs are usually just "fre"), so a track is recognised by a
// region-tagged language ("fr-CA") or by a word of its language or title
// ("VFQ", "Latino").
type regionalVariant struct {
	language string   // canonical ISO 639-2/T code
	regions  []string // region subtags that select this variant ("ca", "419")
	words    []string // lower-case, accent-free label words
}

var regionalVariants = []regionalVariant{
	{language: "fra", regions: []string{"ca", "qc"},
		words: []string{"vfq", "quebec", "quebecois", "quebecoise", "canada", "canadian", "canadien", "canadienne"}},
	{language: "fra", regions: []string{"fr", "be", "ch"},
		words: []string{"vff", "vfi", "france", "parisian"}},
	{language: "por", regions: []string{"br"},
		words: []string{"brazil", "brazilian", "brasil", "brasileiro", "ptbr"}},
	{language: "por", regions: []string{"pt"},
		words: []string{"portugal", "ptpt", "europeu"}},
	{language: "spa", regions: []string{"419", "mx", "latam", "la", "ar", "co", "cl", "us"},
		words: []string{"latino", "latin", "latam", "latinoamerica", "latinoamericano", "mexico", "mexican", "neutro"}},
	{language: "spa", regions: []string{"es"},
		words: []string{"castilian", "castellano", "spain", "espana", "iberian"}},
}

var accentFolder = strings.NewReplacer("é", "e", "è", "e", "ê", "e", "ç", "c", "ñ", "n", "ã", "a", "á", "a", "í", "i", "ó", "o", "ú", "u")

// splitRegion splits "fr-CA" / "fre_ca" into its language and lower-case
// region subtag; the region is empty when there is none.
func splitRegion(tag string) (language, region string) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		return tag[:i], tag[i+1:]
	}
	return tag, ""
}

// preferredVariant returns the regional variant a preference such as "fr-CA"
// asks for, or nil for a plain language preference.
func preferredVariant(preference string) *regionalVariant {
	language, region := splitRegion(SanitizeLanguageCode(preference))
	if region == "" {
		return nil
	}
	code := canonicalLanguageCode(language)
	for i := range regionalVariants {
		v := &regionalVariants[i]
		if v.language != code {
			continue
		}
		for _, r := range v.regions {
			if r == region {
				return v
			}
		}
	}
	return nil
}

// matches reports whether an audio stream is this regional variant.
func (v *regionalVariant) matches(stream models.AudioStreamInfo) bool {
	if _, region := splitRegion(stream.Language); region != "" {
		for _, r := range v.regions {
			if r == region {
				return true
			}
		}
	}
	label := accentFolder.Replace(strings.ToLower(stream.Language + " " + stream.Title))
	for _, word := range strings.FieldsFunc(label, func(r rune) bool { return r < 'a' || r > 'z' }) {
		for _, w := range v.words {
			if word == w {
				return true
			}
		}
	}
	return false
}

// regionalStreams returns the non-commentary streams of the variant a
// region-tagged preference asks for, or nil when the preference names no known
// variant. A regional commentary never wins over the language's main dub.
func regionalStreams(streams []models.AudioStreamInfo, preference string) []models.AudioStreamInfo {
	variant := preferredVariant(preference)
	if variant == nil {
		return nil
	}
	var out []models.AudioStreamInfo
	for _, stream := range streams {
		if variant.matches(stream) && !IsCommentaryTrack(stream.Title) {
			out = append(out, stream)
		}
	}
	return out
}
