package simkl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/apiusage"
)

var apiBaseURL = "https://api.simkl.com"

const (
	appName    = "mediastorm"
	appVersion = "1.0"
	userAgent  = "mediastorm/1.0"
)

// SetBaseURLForTest overrides the Simkl API base URL.
func SetBaseURLForTest(url string) {
	apiBaseURL = url
}

// Client handles Simkl API requests.
type Client struct {
	httpClient         *http.Client
	mu                 sync.Mutex
	lastPostByClientID map[string]time.Time
}

// NewClient creates a Simkl API client.
func NewClient() *Client {
	return &Client{
		httpClient:         apiusage.TrackClient(&http.Client{Timeout: 15 * time.Second}, "Simkl", "API request"),
		lastPostByClientID: make(map[string]time.Time),
	}
}

// SetHTTPClientForTest overrides the HTTP client.
func (c *Client) SetHTTPClientForTest(httpClient *http.Client) {
	if httpClient != nil {
		c.httpClient = httpClient
	}
}

type IDs struct {
	Simkl int    `json:"simkl,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

type Movie struct {
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	IDs   IDs    `json:"ids,omitempty"`
}

type Show struct {
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	IDs   IDs    `json:"ids,omitempty"`
}

type Episode struct {
	Season int `json:"season,omitempty"`
	Number int `json:"number,omitempty"`
}

type ScrobbleRequest struct {
	Progress float64  `json:"progress"`
	Movie    *Movie   `json:"movie,omitempty"`
	Show     *Show    `json:"show,omitempty"`
	Episode  *Episode `json:"episode,omitempty"`
}

type ScrobbleResponse struct {
	ID       int64   `json:"id,omitempty"`
	Action   string  `json:"action,omitempty"`
	Progress float64 `json:"progress,omitempty"`
}

type SyncHistoryMovie struct {
	WatchedAt string `json:"watched_at,omitempty"`
	Title     string `json:"title,omitempty"`
	Year      int    `json:"year,omitempty"`
	IDs       IDs    `json:"ids,omitempty"`
}

type SyncHistoryShow struct {
	Title   string              `json:"title,omitempty"`
	Year    int                 `json:"year,omitempty"`
	IDs     IDs                 `json:"ids,omitempty"`
	Seasons []SyncHistorySeason `json:"seasons,omitempty"`
}

type SyncHistorySeason struct {
	Number   int                  `json:"number"`
	Episodes []SyncHistoryEpisode `json:"episodes,omitempty"`
}

type SyncHistoryEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at,omitempty"`
}

type SyncHistoryRequest struct {
	Movies []SyncHistoryMovie `json:"movies,omitempty"`
	Shows  []SyncHistoryShow  `json:"shows,omitempty"`
}

type ActivityResponse map[string]interface{}

type AllItemsResponse struct {
	Movies []json.RawMessage `json:"movies,omitempty"`
	Shows  []json.RawMessage `json:"shows,omitempty"`
	Anime  []json.RawMessage `json:"anime,omitempty"`
	Raw    json.RawMessage   `json:"-"`
}

type apiCredentials struct {
	clientID    string
	accessToken string
}

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type PinResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type PinTokenResponse struct {
	AccessToken string `json:"access_token"`
	UserID      string `json:"user_id,omitempty"`
	Result      string `json:"result,omitempty"`
	Message     string `json:"message,omitempty"`
}

func (c apiCredentials) valid() bool {
	return c.clientID != "" && c.accessToken != ""
}

func (c *Client) ScrobbleStart(clientID, accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(apiCredentials{clientID: clientID, accessToken: accessToken}, "start", req)
}

func (c *Client) ScrobblePause(clientID, accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(apiCredentials{clientID: clientID, accessToken: accessToken}, "pause", req)
}

func (c *Client) ScrobbleStop(clientID, accessToken string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	return c.scrobble(apiCredentials{clientID: clientID, accessToken: accessToken}, "stop", req)
}

// SyncHistoryResponse is the body returned by POST /sync/history.
// Critical: if requested season/episode numbers don't match Simkl's catalog,
// they appear under NotFound — but Simkl may STILL mark the whole ended show
// completed (all aired episodes). Callers must check NotFound and undo.
type SyncHistoryResponse struct {
	Added struct {
		Movies   int `json:"movies"`
		Shows    int `json:"shows"`
		Episodes int `json:"episodes"`
		Statuses []struct {
			Request struct {
				IDs  IDs    `json:"ids"`
				Type string `json:"type"`
			} `json:"request"`
			Response struct {
				Status    string `json:"status"`
				SimklType string `json:"simkl_type"`
			} `json:"response"`
		} `json:"statuses"`
	} `json:"added"`
	NotFound struct {
		Movies   []json.RawMessage `json:"movies"`
		Shows    []json.RawMessage `json:"shows"`
		Episodes []struct {
			IDs     IDs `json:"ids"`
			Seasons []struct {
				Number   int `json:"number"`
				Episodes []struct {
					Number int `json:"number"`
				} `json:"episodes"`
			} `json:"seasons"`
		} `json:"episodes"`
	} `json:"not_found"`
}

func (c *Client) SyncHistory(clientID, accessToken string, req SyncHistoryRequest) error {
	_, err := c.SyncHistorySafe(clientID, accessToken, req)
	return err
}

// SyncHistoryDetailed posts /sync/history and returns the parsed response
// without applying the not_found safety undo.
func (c *Client) SyncHistoryDetailed(clientID, accessToken string, req SyncHistoryRequest) (*SyncHistoryResponse, error) {
	var out SyncHistoryResponse
	if _, err := c.post(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/history", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SyncHistorySafe posts /sync/history and undoes accidental full-series
// completes when every requested episode for a show is not_found.
//
// Simkl docs: adding an ended show without valid episodes marks the entire
// series completed. Unmatched S/E numbers put episodes in not_found but the
// show can still be fully completed (Columbo 61/61, DBZ 291/291).
func (c *Client) SyncHistorySafe(clientID, accessToken string, req SyncHistoryRequest) (*SyncHistoryResponse, error) {
	resp, err := c.SyncHistoryDetailed(clientID, accessToken, req)
	if err != nil {
		return resp, err
	}
	if undone := c.UndoShowsWithAllEpisodesNotFound(clientID, accessToken, req.Shows, resp); undone > 0 {
		log.Printf("[simkl] undid %d accidental full-series complete(s) after episode not_found", undone)
	}
	return resp, nil
}

// RemoveFromHistory posts /sync/history/remove (undo accidental full-show completes).
func (c *Client) RemoveFromHistory(clientID, accessToken string, req SyncHistoryRequest) error {
	_, err := c.post(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/history/remove", req, nil)
	return err
}

// UndoShowsWithAllEpisodesNotFound removes shows where every episode we
// requested landed in not_found (Simkl may have completed the whole series).
// Partial not_found (some episodes matched) does not remove the show.
func (c *Client) UndoShowsWithAllEpisodesNotFound(clientID, accessToken string, requested []SyncHistoryShow, resp *SyncHistoryResponse) int {
	if c == nil || resp == nil || len(resp.NotFound.Episodes) == 0 || len(requested) == 0 {
		return 0
	}

	// Build not_found set per show: "s:e" keys.
	type nfShow struct {
		ids IDs
		eps map[string]bool
	}
	var notFound []nfShow
	for _, nf := range resp.NotFound.Episodes {
		eps := make(map[string]bool)
		for _, season := range nf.Seasons {
			for _, ep := range season.Episodes {
				eps[fmt.Sprintf("%d:%d", season.Number, ep.Number)] = true
			}
		}
		if len(eps) == 0 {
			continue
		}
		notFound = append(notFound, nfShow{ids: nf.IDs, eps: eps})
	}
	if len(notFound) == 0 {
		return 0
	}

	undone := 0
	for _, show := range requested {
		intended := make(map[string]bool)
		for _, season := range show.Seasons {
			for _, ep := range season.Episodes {
				intended[fmt.Sprintf("%d:%d", season.Number, ep.Number)] = true
			}
		}
		if len(intended) == 0 {
			// Show-level add with no episodes — that path intentionally
			// completes ended series; not our partial-export case.
			continue
		}

		var match *nfShow
		for i := range notFound {
			if idsOverlap(show.IDs, notFound[i].ids) {
				match = &notFound[i]
				break
			}
		}
		if match == nil {
			continue
		}

		// Undo only when every intended episode was not_found.
		allMissing := true
		for key := range intended {
			if !match.eps[key] {
				allMissing = false
				break
			}
		}
		if !allMissing {
			continue
		}

		removeReq := SyncHistoryRequest{Shows: []SyncHistoryShow{{IDs: show.IDs}}}
		// Prefer not_found IDs if they include a stronger match set.
		if match.ids != (IDs{}) {
			removeReq.Shows[0].IDs = match.ids
		}
		if err := c.RemoveFromHistory(clientID, accessToken, removeReq); err != nil {
			log.Printf("[simkl] undo full-complete failed for tmdb=%d tvdb=%d imdb=%s: %v",
				show.IDs.TMDB, show.IDs.TVDB, show.IDs.IMDB, err)
			continue
		}
		log.Printf("[simkl] undid accidental full-series complete (all %d requested episode(s) not_found) tmdb=%d tvdb=%d imdb=%s",
			len(intended), show.IDs.TMDB, show.IDs.TVDB, show.IDs.IMDB)
		undone++
	}
	return undone
}

func idsOverlap(a, b IDs) bool {
	if a.Simkl > 0 && a.Simkl == b.Simkl {
		return true
	}
	if a.TMDB > 0 && a.TMDB == b.TMDB {
		return true
	}
	if a.TVDB > 0 && a.TVDB == b.TVDB {
		return true
	}
	if a.IMDB != "" && b.IMDB != "" && strings.EqualFold(a.IMDB, b.IMDB) {
		return true
	}
	return false
}

func (c *Client) GetActivities(clientID, accessToken string) (ActivityResponse, error) {
	var out ActivityResponse
	if err := c.get(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/activities", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetInitialSyncItems(clientID, accessToken, bucket string) (*AllItemsResponse, error) {
	if bucket != "movies" && bucket != "shows" && bucket != "anime" {
		return nil, fmt.Errorf("unsupported simkl sync bucket: %s", bucket)
	}
	q := url.Values{}
	q.Set("extended", "full")
	q.Set("episode_watched_at", "yes")

	var out AllItemsResponse
	if err := c.get(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/"+bucket, q, &out); err != nil {
		return nil, err
	}
	if len(out.Raw) > 0 && out.Movies == nil && out.Shows == nil && out.Anime == nil {
		var items []json.RawMessage
		if err := json.Unmarshal(out.Raw, &items); err == nil {
			switch bucket {
			case "movies":
				out.Movies = items
			case "shows":
				out.Shows = items
			case "anime":
				out.Anime = items
			}
		}
	}
	return &out, nil
}

// ListItem is a normalized entry from a Simkl status bucket, suitable for
// building home-shelf curated lists.
type ListItem struct {
	Title     string
	Year      int
	MediaType string // "movie" or "show"
	Status    string // plantowatch, watching, completed, hold, dropped
	IDs       IDs
}

// validSimklStatuses enumerates the Simkl list/status buckets.
var validSimklStatuses = map[string]bool{
	"plantowatch": true,
	"watching":    true,
	"completed":   true,
	"hold":        true,
	"dropped":     true,
}

// GetListItems fetches a user's Simkl list for a given media bucket
// (movies/shows/anime) filtered to a single status bucket. An empty status
// returns every item in the media bucket.
func (c *Client) GetListItems(clientID, accessToken, mediaType, status string) ([]ListItem, error) {
	if mediaType != "movies" && mediaType != "shows" && mediaType != "anime" {
		return nil, fmt.Errorf("unsupported simkl media bucket: %s", mediaType)
	}
	status = normalizeSimklStatus(status)
	if status != "" && !validSimklStatuses[status] {
		return nil, fmt.Errorf("unsupported simkl status bucket: %s", status)
	}

	// The plain /sync/{type} endpoint only returns watched history. Watchlist
	// statuses (plan-to-watch, watching, hold, dropped) live under
	// /sync/all-items/{type}[/{status}].
	path := "/sync/all-items/" + mediaType
	if status != "" {
		path += "/" + status
	}
	q := url.Values{}
	q.Set("extended", "full")

	var resp struct {
		Movies []simklListItemRaw `json:"movies"`
		Shows  []simklListItemRaw `json:"shows"`
		Anime  []simklListItemRaw `json:"anime"`
	}
	if err := c.get(apiCredentials{clientID: clientID, accessToken: accessToken}, path, q, &resp); err != nil {
		return nil, err
	}

	var raws []simklListItemRaw
	switch mediaType {
	case "movies":
		raws = resp.Movies
	case "shows":
		raws = resp.Shows
	case "anime":
		raws = resp.Anime
	}

	items := make([]ListItem, 0, len(raws))
	for _, raw := range raws {
		item, ok := raw.normalize(mediaType)
		if !ok {
			continue
		}
		if status != "" && item.Status != status {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func normalizeSimklStatus(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch s {
	case "", "all":
		return ""
	case "plan_to_watch", "plan-to-watch":
		return "plantowatch"
	case "on_hold", "on-hold":
		return "hold"
	default:
		return s
	}
}

// flexInt unmarshals a JSON value that may be a number or a numeric string.
// Simkl's all-items endpoint returns tmdb/tvdb ids as strings.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil // tolerate non-numeric ids rather than failing the whole item
	}
	*f = flexInt(n)
	return nil
}

type simklIDsRaw struct {
	Simkl int     `json:"simkl"`
	IMDB  string  `json:"imdb"`
	TMDB  flexInt `json:"tmdb"`
	TVDB  flexInt `json:"tvdb"`
}

func (r simklIDsRaw) toIDs() IDs {
	return IDs{Simkl: r.Simkl, IMDB: r.IMDB, TMDB: int(r.TMDB), TVDB: int(r.TVDB)}
}

type simklMediaRaw struct {
	Title string      `json:"title"`
	Year  int         `json:"year"`
	IDs   simklIDsRaw `json:"ids"`
}

type simklListItemRaw struct {
	Status string         `json:"status"`
	Movie  *simklMediaRaw `json:"movie"`
	Show   *simklMediaRaw `json:"show"`
	Anime  *simklMediaRaw `json:"anime"`
	// Fallback when the bucket returns bare media objects (no wrapper).
	Title string      `json:"title"`
	Year  int         `json:"year"`
	IDs   simklIDsRaw `json:"ids"`
}

func (r simklListItemRaw) normalize(mediaType string) (ListItem, bool) {
	item := ListItem{Status: normalizeSimklStatus(r.Status)}
	switch {
	case r.Movie != nil:
		item.Title, item.Year, item.IDs, item.MediaType = r.Movie.Title, r.Movie.Year, r.Movie.IDs.toIDs(), "movie"
	case r.Show != nil:
		item.Title, item.Year, item.IDs, item.MediaType = r.Show.Title, r.Show.Year, r.Show.IDs.toIDs(), "show"
	case r.Anime != nil:
		item.Title, item.Year, item.IDs, item.MediaType = r.Anime.Title, r.Anime.Year, r.Anime.IDs.toIDs(), "show"
	default:
		item.Title, item.Year, item.IDs = r.Title, r.Year, r.IDs.toIDs()
		if mediaType == "movies" {
			item.MediaType = "movie"
		} else {
			item.MediaType = "show"
		}
	}

	if item.Title == "" && item.IDs == (IDs{}) {
		return ListItem{}, false
	}
	return item, true
}

func (c *Client) GetAllItemsSince(clientID, accessToken, dateFrom string) (*AllItemsResponse, error) {
	q := url.Values{}
	if dateFrom != "" {
		q.Set("date_from", dateFrom)
	}
	q.Set("extended", "full")
	q.Set("episode_watched_at", "yes")

	var out AllItemsResponse
	if err := c.get(apiCredentials{clientID: clientID, accessToken: accessToken}, "/sync/all-items", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) StartPINAuth(clientID string) (*PinResponse, error) {
	endpoint, err := buildAPIURL("/oauth/pin", clientID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create pin request: %w", err)
	}
	req.Header.Set("simkl-api-key", clientID)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simkl pin request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("simkl pin request returned %d: %s", resp.StatusCode, string(respBody))
	}

	var pin PinResponse
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, fmt.Errorf("decode pin response: %w", err)
	}
	return &pin, nil
}

func (c *Client) CheckPINAuth(clientID, userCode string) (*PinTokenResponse, error) {
	endpoint, err := buildAPIURL("/oauth/pin/"+url.PathEscape(userCode), clientID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create pin check request: %w", err)
	}
	req.Header.Set("simkl-api-key", clientID)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simkl pin check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("simkl pin check returned %d: %s", resp.StatusCode, string(respBody))
	}

	var token PinTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode pin check response: %w", err)
	}
	return &token, nil
}

func (c *Client) ExchangeCode(clientID, clientSecret, redirectURI, code string) (*TokenResponse, error) {
	payload := map[string]string{
		"code":          code,
		"client_id":     clientID,
		"client_secret": clientSecret,
		"redirect_uri":  redirectURI,
		"grant_type":    "authorization_code",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	endpoint, err := buildAPIURL("/oauth/token", clientID)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("simkl-api-key", clientID)
	req.Header.Set("User-Agent", userAgent)

	c.waitForPostSlot(clientID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simkl token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("simkl token exchange returned %d: %s", resp.StatusCode, string(respBody))
	}

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &token, nil
}

func (c *Client) scrobble(creds apiCredentials, action string, req ScrobbleRequest) (*ScrobbleResponse, error) {
	var out ScrobbleResponse
	_, err := c.post(creds, "/scrobble/"+action, req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) get(creds apiCredentials, path string, extraQuery url.Values, out interface{}) error {
	if !creds.valid() {
		return errors.New("simkl credentials not configured")
	}

	endpoint, err := buildAPIURL(path, creds.clientID)
	if err != nil {
		return err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse simkl url: %w", err)
	}
	q := u.Query()
	for key, values := range extraQuery {
		for _, value := range values {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("simkl-api-key", creds.clientID)
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("simkl api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("simkl %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if allItems, ok := out.(*AllItemsResponse); ok {
		allItems.Raw = append(allItems.Raw[:0], body...)
		if len(body) > 0 && body[0] == '[' {
			return nil
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) post(creds apiCredentials, path string, payload interface{}, out interface{}) (int, error) {
	if !creds.valid() {
		return 0, errors.New("simkl credentials not configured")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}

	endpoint, err := buildAPIURL(path, creds.clientID)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("simkl-api-key", creds.clientID)
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("User-Agent", userAgent)

	c.waitForPostSlot(creds.clientID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("simkl api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("simkl %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func buildAPIURL(path, clientID string) (string, error) {
	u, err := url.Parse(apiBaseURL + path)
	if err != nil {
		return "", fmt.Errorf("parse simkl url: %w", err)
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("app-name", appName)
	q.Set("app-version", appVersion)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) waitForPostSlot(clientID string) {
	if clientID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastPostByClientID == nil {
		c.lastPostByClientID = make(map[string]time.Time)
	}

	now := time.Now()
	if last, ok := c.lastPostByClientID[clientID]; ok {
		if wait := time.Second - now.Sub(last); wait > 0 {
			time.Sleep(wait)
			now = time.Now()
		}
	}
	c.lastPostByClientID[clientID] = now
}
