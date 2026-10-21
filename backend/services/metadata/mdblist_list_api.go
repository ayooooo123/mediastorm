package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

const (
	mdblistListAPIBaseURL  = "https://api.mdblist.com"
	mdblistListAPIPageSize = 1000
	// mdblistListAPIMaxItems bounds pagination; Home rows never show more.
	mdblistListAPIMaxItems = 5000
	// mdblistListAPIFailureTTL keeps a failing API list on the public JSON
	// source for one source-cache window instead of retrying every request.
	mdblistListAPIFailureTTL = shelfSourceTTL
	// mdblistListAPISourceTTL is longer than the public JSON window: each
	// refresh costs API quota shared with ratings lookups, and curated list
	// order rarely changes within an hour.
	mdblistListAPISourceTTL = 60 * time.Minute
	// mdblistQuotaReserveFraction keeps the last part of the daily quota for
	// ratings; list sources use public JSON once remaining drops below it.
	mdblistQuotaReserveFraction = 0.10
	// mdblistQuotaDefaultHold applies when a low-quota response has no reset.
	mdblistQuotaDefaultHold = time.Hour
)

// mdblistListAPIFailures records when the API source last failed per list URL.
var mdblistListAPIFailures sync.Map

// mdblistAPIListItem is one movie/show row from the MDBList list items API
// requested with append_to_response=poster,description,genres,ratings.
type mdblistAPIListItem struct {
	ID          int64  `json:"id"`
	Rank        int    `json:"rank"`
	Adult       int    `json:"adult"`
	Title       string `json:"title"`
	MediaType   string `json:"mediatype"`
	IMDBID      string `json:"imdb_id"`
	TVDBID      int64  `json:"tvdb_id"`
	ReleaseYear int    `json:"release_year"`
	ReleaseDate string `json:"release_date"`
	Runtime     int    `json:"runtime"`
	Poster      string `json:"poster"`
	Description string `json:"description"`
	IDs         struct {
		IMDB string `json:"imdb"`
		TMDB int64  `json:"tmdb"`
		TVDB int64  `json:"tvdb"`
	} `json:"ids"`
	Genres  []string             `json:"genres"`
	Ratings []mdblistRatingEntry `json:"ratings"`
}

type mdblistRatingEntry struct {
	Source string   `json:"source"`
	Value  *float64 `json:"value"`
	Score  *float64 `json:"score"`
	Votes  *int     `json:"votes"`
}

type mdblistAPIListPage struct {
	Movies []mdblistAPIListItem `json:"movies"`
	Shows  []mdblistAPIListItem `json:"shows"`
}

// mdblistAPIListPath maps a public mdblist.com list URL to the list items API
// path ("lists/{user}/{slug}" or "lists/{id}"). Other hosts are not handled.
func mdblistAPIListPath(listURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(listURL))
	if err != nil || !strings.EqualFold(parsed.Hostname(), "mdblist.com") {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) > 0 && strings.EqualFold(parts[len(parts)-1], "json") {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 || parts[0] != "lists" {
		return "", false
	}
	switch len(parts) {
	case 2:
		if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
			return "", false
		}
		return "lists/" + parts[1], true
	case 3:
		if parts[1] == "" || parts[2] == "" {
			return "", false
		}
		return "lists/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(parts[2]), true
	default:
		return "", false
	}
}

// listAPIKey returns the configured MDBList API key. Lists only need the key;
// they do not depend on ratings being enabled.
func (c *mdblistClient) listAPIKey() string {
	if c == nil {
		return ""
	}
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	return strings.TrimSpace(c.apiKey)
}

// fetchListItems reads a whole list through the MDBList API with poster,
// description, genres and ratings attached, so shelves need no per-item
// provider calls. Pages are requested until x-has-more is false.
func (c *mdblistClient) fetchListItems(ctx context.Context, apiKey, listPath string) ([]mdblistItem, error) {
	var rows []mdblistAPIListItem
	for offset := 0; offset < mdblistListAPIMaxItems; offset += mdblistListAPIPageSize {
		params := url.Values{
			"apikey":             []string{apiKey},
			"limit":              []string{strconv.Itoa(mdblistListAPIPageSize)},
			"offset":             []string{strconv.Itoa(offset)},
			"append_to_response": []string{"poster,description,genres,ratings"},
		}
		endpoint := fmt.Sprintf("%s/%s/items?%s", mdblistListAPIBaseURL, listPath, params.Encode())
		page, hasMore, err := c.fetchListPage(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page.Movies...)
		rows = append(rows, page.Shows...)
		if !hasMore || len(page.Movies)+len(page.Shows) == 0 {
			break
		}
	}
	// Movies and shows arrive in separate arrays; rank restores list order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Rank < rows[j].Rank })
	if len(rows) > mdblistListAPIMaxItems {
		rows = rows[:mdblistListAPIMaxItems]
	}
	items := make([]mdblistItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toItem())
	}
	return items, nil
}

func (c *mdblistClient) fetchListPage(ctx context.Context, endpoint string) (mdblistAPIListPage, bool, error) {
	var page mdblistAPIListPage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return page, false, err
	}
	// The MDBList API rejects requests without a client User-Agent.
	req.Header.Set("User-Agent", "mediastorm/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := c.listHTTPClient.Do(req)
	if err != nil {
		return page, false, fmt.Errorf("mdblist list api request: %w", err)
	}
	defer resp.Body.Close()
	c.quota.record(resp.Header, time.Now())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return page, false, fmt.Errorf("mdblist list api returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return page, false, fmt.Errorf("mdblist list api decode: %w", err)
	}
	return page, strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Has-More")), "true"), nil
}

func (row mdblistAPIListItem) toItem() mdblistItem {
	item := mdblistItem{
		ID:          int(row.ID),
		Rank:        row.Rank,
		Adult:       row.Adult,
		Title:       row.Title,
		IMDBID:      strings.TrimSpace(row.IMDBID),
		MediaType:   row.MediaType,
		ReleaseYear: row.ReleaseYear,
		PosterURL:   mdblistPosterURL(row.Poster),
		Overview:    strings.TrimSpace(row.Description),
		Genres:      mdblistGenreNames(row.Genres),
		ReleaseDate: strings.TrimSpace(row.ReleaseDate),
		Runtime:     row.Runtime,
		Ratings:     convertMDBListRatings(row.Ratings),
	}
	if item.IMDBID == "" {
		item.IMDBID = strings.TrimSpace(row.IDs.IMDB)
	}
	tmdbID := row.IDs.TMDB
	if tmdbID <= 0 {
		tmdbID = row.ID
	}
	if tmdbID > 0 {
		item.TMDBID = &tmdbID
	}
	tvdbID := row.IDs.TVDB
	if tvdbID <= 0 {
		tvdbID = row.TVDBID
	}
	if tvdbID > 0 {
		item.TVDBID = &tvdbID
	}
	return item
}

// mdblistPosterURL upgrades MDBList's w200 TMDB thumbnail to the card size.
func mdblistPosterURL(poster string) string {
	poster = strings.TrimSpace(poster)
	if !strings.HasPrefix(poster, "http") {
		return ""
	}
	return strings.Replace(poster, "image.tmdb.org/t/p/w200/", "image.tmdb.org/t/p/w342/", 1)
}

var mdblistGenreOverrides = map[string]string{
	"science-fiction":  "Science Fiction",
	"sci-fi":           "Science Fiction",
	"tv-movie":         "TV Movie",
	"action-adventure": "Action & Adventure",
	"sci-fi-fantasy":   "Sci-Fi & Fantasy",
	"war-politics":     "War & Politics",
}

// mdblistGenreNames converts MDBList genre slugs ("science-fiction") to the
// title-cased names TMDB/TVDB enrichment produces ("Science Fiction").
func mdblistGenreNames(slugs []string) []string {
	if len(slugs) == 0 {
		return nil
	}
	names := make([]string, 0, len(slugs))
	seen := make(map[string]bool, len(slugs))
	for _, slug := range slugs {
		slug = strings.ToLower(strings.TrimSpace(slug))
		if slug == "" {
			continue
		}
		name, ok := mdblistGenreOverrides[slug]
		if !ok {
			words := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
			for i, word := range words {
				words[i] = strings.ToUpper(word[:1]) + word[1:]
			}
			name = strings.Join(words, " ")
		}
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// convertMDBListRatings converts MDBList rating rows into the all-sources form
// stored by the ratings cache. Sources without a value are dropped.
func convertMDBListRatings(entries []mdblistRatingEntry) []models.Rating {
	var ratings []models.Rating
	for _, r := range entries {
		if r.Value == nil || *r.Value == 0 {
			continue
		}
		internalSource := r.Source
		if mapped, ok := apiSourceToInternal[r.Source]; ok {
			internalSource = mapped
		}
		maxValue := 10.0
		if info, ok := ratingSourceInfo[r.Source]; ok {
			maxValue = info.max
		}
		ratings = append(ratings, models.Rating{Source: internalSource, Value: *r.Value, Max: maxValue})
	}
	return ratings
}

// ratingsListSeedKey stores ratings carried by an MDBList list response. They
// are a subset of the per-title endpoint, so they never satisfy details pages.
func ratingsListSeedKey(imdbID, mediaType string) string {
	return ratingsDiskCacheKey(imdbID, mediaType) + "_list"
}

// seedMDBListListRatings stores list-supplied ratings so list rating
// enrichment does not issue one MDBList call per title.
func (s *Service) seedMDBListListRatings(items []mdblistItem) {
	if s == nil || s.ratingsCache == nil {
		return
	}
	seeded := 0
	for _, item := range items {
		imdbID := strings.TrimSpace(item.IMDBID)
		if imdbID == "" || len(item.Ratings) == 0 {
			continue
		}
		if !strings.HasPrefix(imdbID, "tt") {
			imdbID = "tt" + imdbID
		}
		mediaType := "movie"
		if mdblistItemMediaType(item) == "series" {
			mediaType = "show"
		}
		if err := s.ratingsCache.set(ratingsListSeedKey(imdbID, mediaType), item.Ratings); err == nil {
			seeded++
		}
	}
	if seeded > 0 {
		log.Printf("[mdblist] seeded list ratings for %d/%d items", seeded, len(items))
	}
}

// fetchCustomListFromAPI reads an mdblist.com list through the MDBList API when
// a key is configured. ok=false means the caller should use the public JSON.
func (s *Service) fetchCustomListFromAPI(ctx context.Context, listURL string) ([]mdblistItem, bool) {
	if s == nil || s.mdblist == nil {
		return nil, false
	}
	apiKey := s.mdblist.listAPIKey()
	listPath, ok := mdblistAPIListPath(listURL)
	if apiKey == "" || !ok {
		return nil, false
	}
	if failedAt, ok := mdblistListAPIFailures.Load(listURL); ok && time.Since(failedAt.(time.Time)) < mdblistListAPIFailureTTL {
		return nil, false
	}
	var items []mdblistItem
	key := customListSourceCacheKey("api", listURL)
	if ok, _ := s.cache.getWithMaxAge(key, &items, mdblistListAPISourceTTL); ok && len(items) > 0 {
		return items, true
	}
	// A refresh spends quota; keep the reserve for ratings and serve the
	// public JSON until the quota resets.
	if s.mdblist.quota.low(time.Now()) {
		return nil, false
	}
	err := s.fetchShelfCached(ctx, key, mdblistListAPISourceTTL, &items, func(ctx context.Context) (any, error) {
		result, err := s.mdblist.fetchListItems(ctx, apiKey, listPath)
		if err != nil {
			return nil, err
		}
		if len(result) == 0 {
			return nil, fmt.Errorf("mdblist list api returned no items")
		}
		s.seedMDBListListRatings(result)
		return result, nil
	})
	if err != nil {
		if ctx.Err() == nil {
			mdblistListAPIFailures.Store(listURL, time.Now())
			log.Printf("[mdblist] list api failed for %s, using public JSON: %v", listURL, err)
		}
		return nil, false
	}
	return items, true
}

func customListSourceCacheKey(source, listURL string) string {
	return cacheKey("shelf-source", "v2", source, listURL)
}

// customListSourceTTL is the freshness window for one list source cache.
func customListSourceTTL(source string) time.Duration {
	if source == "api" {
		return mdblistListAPISourceTTL
	}
	return shelfSourceTTL
}

// mdblistQuota tracks the API quota reported by MDBList response headers.
// The quota is per key and shared by list sources and ratings lookups.
type mdblistQuota struct {
	mu        sync.Mutex
	limit     int
	remaining int
	known     bool
	resetAt   time.Time
	lowLogged bool
}

// record stores the x-ratelimit-* headers of one MDBList API response.
func (q *mdblistQuota) record(header http.Header, now time.Time) {
	if q == nil || header == nil {
		return
	}
	remaining, errRemaining := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
	limit, errLimit := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Limit")))
	if errRemaining != nil || errLimit != nil || limit <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limit, q.remaining, q.known = limit, remaining, true
	q.resetAt = parseMDBListRateLimitReset(header.Get("X-RateLimit-Reset"), now)
	if q.resetAt.IsZero() {
		q.resetAt = now.Add(mdblistQuotaDefaultHold)
	}
	if float64(remaining) >= float64(limit)*mdblistQuotaReserveFraction {
		q.lowLogged = false
	}
}

// low reports whether the remaining quota is inside the ratings reserve. The
// first low report per quota window is logged.
func (q *mdblistQuota) low(now time.Time) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.known || float64(q.remaining) >= float64(q.limit)*mdblistQuotaReserveFraction {
		return false
	}
	if !now.Before(q.resetAt) {
		// The window has reset; the next API response refreshes the counters.
		q.known = false
		q.lowLogged = false
		return false
	}
	if !q.lowLogged {
		q.lowLogged = true
		log.Printf("[mdblist] API quota low (%d/%d remaining, resets %s); list sources use public JSON until reset",
			q.remaining, q.limit, q.resetAt.Format(time.RFC3339))
	}
	return true
}

// parseMDBListRateLimitReset accepts a Unix timestamp, a delay in seconds or
// an HTTP/RFC3339 date. An unparseable value returns the zero time.
func parseMDBListRateLimitReset(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
		// Values past 2001 in seconds are absolute timestamps; smaller ones
		// are a delay from now.
		if n > 1_000_000_000 {
			return time.Unix(n, 0)
		}
		return now.Add(time.Duration(n) * time.Second)
	}
	for _, layout := range []string{time.RFC3339, http.TimeFormat} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}
