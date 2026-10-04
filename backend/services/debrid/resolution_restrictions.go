package debrid

import (
	"fmt"
	"strconv"
	"strings"

	"novastream/config"
	"novastream/models"
	resultfilter "novastream/utils/filter"
)

const realDebridRestrictedTermsFilterAttribute = "realDebridRestrictedTermsFilterEnabled"

var compiledRealDebridRestrictedReleaseTerms = resultfilter.CompileTerms([]string{resultfilter.RealDebridRestrictedReleaseTerm})

// RestrictedTermError reports a local provider eligibility decision. No
// provider request was made, but trying another source is the right recovery.
type RestrictedTermError struct {
	Provider string
	Title    string
	Term     string
}

func (e *RestrictedTermError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s resolution skipped: release title matches restricted term %q", e.Provider, e.Term)
}

func realDebridRestrictionForCandidate(settings config.Settings, provider config.DebridProviderSettings, candidate models.NZBResult) error {
	enabled := settings.Filtering.RealDebridRestrictedTermsFilterEnabled
	if scoped, err := strconv.ParseBool(strings.TrimSpace(candidate.Attributes[realDebridRestrictedTermsFilterAttribute])); err == nil {
		enabled = scoped
	}
	if !enabled || !strings.EqualFold(strings.TrimSpace(provider.Provider), "realdebrid") {
		return nil
	}

	title := strings.TrimSpace(candidate.Title)
	// The original filename is authoritative: a display title may have changed
	// capitalization or separators, both of which now affect RD eligibility.
	if rawTitle := strings.TrimSpace(candidate.Attributes["raw_title"]); rawTitle != "" {
		title = rawTitle
	}
	if matched := resultfilter.MatchedTerm(title, compiledRealDebridRestrictedReleaseTerms); matched != "" {
		return &RestrictedTermError{Provider: provider.Name, Title: candidate.Title, Term: matched}
	}
	return nil
}

func annotateRealDebridRestrictedTermsFilter(results []models.NZBResult, enabled bool) {
	value := strconv.FormatBool(enabled)
	for i := range results {
		if results[i].Attributes == nil {
			results[i].Attributes = map[string]string{}
		}
		results[i].Attributes[realDebridRestrictedTermsFilterAttribute] = value
	}
}
