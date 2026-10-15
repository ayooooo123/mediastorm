package metadata

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"novastream/models"
)

// searchMatchTier classifies how well a title's name matches the query.
type searchMatchTier int

const (
	searchMatchNone searchMatchTier = iota
	searchMatchSubstring
	searchMatchAllWords
	searchMatchPrefix
	searchMatchSegmentExact
	searchMatchExact
)

// Relevance bonuses share the log1p(popularity) scale, so a better tier wins
// unless the other title is substantially more popular. Exact matches keep a
// clear lead (e^0.75 ≈ 2x over a prefix match, e^1.75 ≈ 5.8x over an
// all-words match), so an obscure exact match can still lose to a blockbuster
// prefix match ("dune" → Dune: Part Two above a 37-vote film also named
// "Dune"). The partial tiers sit close together so popularity mostly decides
// between them: "breaking bad" → El Camino: A Breaking Bad Movie above the
// obscure "Daniel Brühl: Breaking Bad" despite its weaker match.
var searchMatchBonus = map[searchMatchTier]float64{
	searchMatchNone:      0,
	searchMatchSubstring: 2.75,
	searchMatchAllWords:  3.25,
	searchMatchPrefix:    4.25,
	// A colon/dash-delimited part of the title equals the query, e.g.
	// "fellowship of the ring" → "The Lord of the Rings: The Fellowship of
	// the Ring". Nearly as strong as an exact match.
	searchMatchSegmentExact: 4.75,
	searchMatchExact:        5,
}

// rankSearchResults orders provider search results by relevance to the query:
// normalized title match quality (case, punctuation, diacritics and leading
// articles ignored) combined with popularity/vote count. Movies, series and
// people are ranked together, so a combined search no longer lists every movie
// before any series. The sort is stable, keeping provider order for ties.
func rankSearchResults(query string, results []models.SearchResult) {
	if len(results) <= 1 {
		return
	}
	q := normalizeSearchText(query)
	if q == "" {
		return
	}
	qStripped := stripLeadingSearchArticle(q)
	qWords := strings.Fields(qStripped)
	scores := make([]float64, len(results))
	for i := range results {
		scores[i] = searchRelevanceScore(q, qStripped, qWords, results[i])
	}
	idx := make([]int, len(results))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return scores[idx[a]] > scores[idx[b]]
	})
	ranked := make([]models.SearchResult, len(results))
	for i, j := range idx {
		ranked[i] = results[j]
	}
	copy(results, ranked)
}

func searchRelevanceScore(q, qStripped string, qWords []string, result models.SearchResult) float64 {
	tier := searchTitleMatchTier(q, qStripped, qWords, result.Title.Name)
	for _, alt := range append([]string{result.Title.OriginalName}, result.Title.AlternateTitles...) {
		if altTier := searchTitleMatchTier(q, qStripped, qWords, alt); altTier > tier {
			// Alternate/original titles count, one tier lower than the
			// display name so the visible match wins ties.
			if altTier > searchMatchSubstring {
				altTier--
			}
			if altTier > tier {
				tier = altTier
			}
		}
	}
	return searchMatchBonus[tier] + math.Log1p(searchPopularity(result))
}

// searchPopularity prefers TMDB popularity/vote count and falls back to the
// provider score (TVDB score, person popularity) when those are absent.
func searchPopularity(result models.SearchResult) float64 {
	if result.Title.Popularity > 0 || result.Title.VoteCount > 0 {
		return math.Max(0, searchRankScore(result.Title.Popularity, result.Title.VoteCount))
	}
	return math.Max(0, float64(result.Score))
}

func searchTitleMatchTier(q, qStripped string, qWords []string, name string) searchMatchTier {
	n := normalizeSearchText(name)
	if n == "" {
		return searchMatchNone
	}
	nStripped := stripLeadingSearchArticle(n)
	switch {
	case n == q || nStripped == qStripped:
		return searchMatchExact
	case strings.HasPrefix(n, q+" ") || strings.HasPrefix(nStripped, qStripped+" "):
		return searchMatchPrefix
	}
	if tier := searchSegmentMatchTier(q, qStripped, name); tier != searchMatchNone {
		return tier
	}
	if len(qWords) > 0 {
		words := make(map[string]struct{})
		for _, w := range strings.Fields(n) {
			words[w] = struct{}{}
		}
		all := true
		for _, w := range qWords {
			if _, ok := words[w]; !ok {
				all = false
				break
			}
		}
		if all {
			return searchMatchAllWords
		}
	}
	if strings.Contains(n, qStripped) {
		return searchMatchSubstring
	}
	return searchMatchNone
}

var searchTitleSegmentSeparators = strings.NewReplacer(":", "\x00", " - ", "\x00", " – ", "\x00", " — ", "\x00", "|", "\x00")

// searchSegmentMatchTier matches the query against the title's colon- or
// dash-delimited parts (franchise prefix, subtitle).
func searchSegmentMatchTier(q, qStripped, name string) searchMatchTier {
	parts := strings.Split(searchTitleSegmentSeparators.Replace(name), "\x00")
	if len(parts) < 2 {
		return searchMatchNone
	}
	for _, part := range parts {
		n := normalizeSearchText(part)
		if n != "" && (n == q || stripLeadingSearchArticle(n) == qStripped) {
			return searchMatchSegmentExact
		}
	}
	return searchMatchNone
}

// normalizeSearchText lowercases, strips diacritics, maps "&" to "and" and
// collapses punctuation/whitespace into single spaces.
func normalizeSearchText(s string) string {
	s = norm.NFD.String(strings.ToLower(strings.TrimSpace(s)))
	var b strings.Builder
	space := true
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r == '\'' || r == '’':
			// Apostrophes join words: "Grey's" → "greys".
			continue
		case r == '&':
			if !space {
				b.WriteByte(' ')
			}
			b.WriteString("and ")
			space = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func stripLeadingSearchArticle(s string) string {
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, article) && len(s) > len(article) {
			return s[len(article):]
		}
	}
	return s
}
