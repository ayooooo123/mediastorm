package jellyfin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"novastream/internal/apiusage"
)

type PlaybackReport struct {
	ItemID        string `json:"ItemId"`
	MediaSourceID string `json:"MediaSourceId,omitempty"`
	PlaySessionID string `json:"PlaySessionId"`
	PositionTicks int64  `json:"PositionTicks"`
	IsPaused      bool   `json:"IsPaused"`
	IsMuted       bool   `json:"IsMuted"`
	CanSeek       bool   `json:"CanSeek"`
	PlayMethod    string `json:"PlayMethod"`
	RepeatMode    string `json:"RepeatMode"`
	PlaybackOrder string `json:"PlaybackOrder"`
	EventName     string `json:"EventName,omitempty"`
}

// Client handles Jellyfin API interactions.
type Client struct {
	httpClient   *http.Client
	streamClient *http.Client
}

// AuthResult contains the result of a Jellyfin authentication.
type AuthResult struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
}

// JellyfinItem represents an item from Jellyfin (movie, series, or episode).
type JellyfinItem struct {
	ID                string                `json:"Id"`
	Name              string                `json:"Name"`
	Type              string                `json:"Type"` // "Movie", "Series", "Episode"
	Year              int                   `json:"ProductionYear"`
	ProviderIDs       map[string]string     `json:"ProviderIds"`
	SeriesName        string                `json:"SeriesName,omitempty"`
	SeasonNum         int                   `json:"ParentIndexNumber,omitempty"`
	EpisodeNum        int                   `json:"IndexNumber,omitempty"`
	DatePlayed        *time.Time            `json:"LastPlayedDate,omitempty"`
	DateCreated       *time.Time            `json:"DateCreated,omitempty"`
	Overview          string                `json:"Overview,omitempty"`
	OfficialRating    string                `json:"OfficialRating,omitempty"`
	RunTimeTicks      int64                 `json:"RunTimeTicks,omitempty"`
	SeriesID          string                `json:"SeriesId,omitempty"`
	ImageTags         map[string]string     `json:"ImageTags,omitempty"`
	BackdropImageTags []string              `json:"BackdropImageTags,omitempty"`
	MediaSources      []JellyfinMediaSource `json:"MediaSources,omitempty"`
}

type JellyfinLibrary struct {
	ID             string `json:"Id"`
	Name           string `json:"Name"`
	CollectionType string `json:"CollectionType"`
}

type JellyfinMediaStream struct {
	Type       string `json:"Type"`
	Codec      string `json:"Codec"`
	Width      int    `json:"Width,omitempty"`
	Height     int    `json:"Height,omitempty"`
	VideoRange string `json:"VideoRange,omitempty"`
}

type JellyfinMediaSource struct {
	ID           string                `json:"Id"`
	Name         string                `json:"Name"`
	Path         string                `json:"Path"`
	Container    string                `json:"Container"`
	Size         int64                 `json:"Size"`
	RunTimeTicks int64                 `json:"RunTimeTicks,omitempty"`
	MediaStreams []JellyfinMediaStream `json:"MediaStreams"`
}

// NewClient creates a new Jellyfin API client.
func NewClient() *Client {
	return &Client{
		httpClient:   apiusage.TrackClient(&http.Client{Timeout: 30 * time.Second}, "Jellyfin", "API request"),
		streamClient: apiusage.TrackClient(&http.Client{}, "Jellyfin", "Media stream"),
	}
}

// authHeader builds the Jellyfin authorization header.
func authHeader(token string) string {
	h := `MediaBrowser Client="mediastorm", Device="server", DeviceId="mediastorm-server", Version="1.0"`
	if token != "" {
		h += fmt.Sprintf(`, Token="%s"`, token)
	}
	return h
}

// Authenticate authenticates with a Jellyfin server using username/password.
func (c *Client) Authenticate(serverURL, username, password string) (*AuthResult, error) {
	serverURL = strings.TrimRight(serverURL, "/")

	body := fmt.Sprintf(`{"Username":%q,"Pw":%q}`, username, password)
	req, err := http.NewRequest(http.MethodPost, serverURL+"/Users/AuthenticateByName", strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(""))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("authentication failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result AuthResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode auth response: %w", err)
	}

	return &result, nil
}

// TestConnection tests connectivity to a Jellyfin server.
func (c *Client) TestConnection(serverURL, token string) error {
	serverURL = strings.TrimRight(serverURL, "/")

	req, err := http.NewRequest(http.MethodGet, serverURL+"/System/Info", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) GetLibraries(serverURL, token, userID string) ([]JellyfinLibrary, error) {
	var result struct {
		Items []JellyfinLibrary `json:"Items"`
	}
	err := c.getJSON(serverURL, token, "/Users/"+url.PathEscape(userID)+"/Views", nil, &result)
	if err != nil {
		return nil, fmt.Errorf("fetch libraries: %w", err)
	}
	return result.Items, nil
}

func (c *Client) GetLibraryItems(serverURL, token, userID, libraryID, collectionType string) ([]JellyfinItem, error) {
	params := url.Values{
		"ParentId": {libraryID}, "Recursive": {"true"},
		"Fields":       {"ProviderIds,Overview,OfficialRating,MediaSources,MediaStreams,ImageTags,BackdropImageTags,RunTimeTicks,DateCreated"},
		"EnableImages": {"true"}, "EnableTotalRecordCount": {"true"},
	}
	if strings.EqualFold(collectionType, "tvshows") {
		params.Set("IncludeItemTypes", "Episode")
	} else if strings.EqualFold(collectionType, "movies") {
		params.Set("IncludeItemTypes", "Movie")
	} else {
		// Mixed, home-video, music-video, and other user-created views can
		// contain several playable item types. Folder and image-only entries
		// are deliberately excluded from the remote playback library.
		params.Set("IncludeItemTypes", "Movie,Episode,Video,MusicVideo,Audio")
	}
	start := 0
	items := []JellyfinItem{}
	for {
		params.Set("StartIndex", fmt.Sprint(start))
		params.Set("Limit", "500")
		var page struct {
			Items []JellyfinItem `json:"Items"`
			Total int            `json:"TotalRecordCount"`
		}
		if err := c.getJSON(serverURL, token, "/Users/"+url.PathEscape(userID)+"/Items", params, &page); err != nil {
			return nil, fmt.Errorf("fetch library items: %w", err)
		}
		for i := range page.Items {
			page.Items[i].ProviderIDs = normalizeProviderIDs(page.Items[i].ProviderIDs)
		}
		items = append(items, page.Items...)
		start += len(page.Items)
		if len(page.Items) == 0 || start >= page.Total {
			break
		}
	}
	return items, nil
}

func (c *Client) OpenStream(ctx context.Context, serverURL, token, itemID, mediaSourceID, method, rangeHeader string) (*http.Response, error) {
	mediaSourceIDs := []string{mediaSourceID}
	if mediaSourceID != "" {
		// Jellyfin media-source IDs may change after a library rescan while the
		// item ID remains valid. If the selected version is stale, retry the item
		// without it and let Jellyfin resolve the current default source.
		mediaSourceIDs = append(mediaSourceIDs, "")
	}

	var lastErr error
	for index, sourceID := range mediaSourceIDs {
		resp, err := c.openStream(ctx, serverURL, token, itemID, sourceID, method, rangeHeader)
		if err != nil {
			return nil, err
		}
		validated, err := validateStreamResponse(resp, method)
		if err == nil {
			return validated, nil
		}
		lastErr = err
		if index == len(mediaSourceIDs)-1 || !retryableStaleMediaSourceResponse(resp) {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) openStream(ctx context.Context, serverURL, token, itemID, mediaSourceID, method, rangeHeader string) (*http.Response, error) {
	params := url.Values{"Static": {"true"}}
	if mediaSourceID != "" {
		params.Set("MediaSourceId", mediaSourceID)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/Videos/" + url.PathEscape(itemID) + "/stream?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(token))
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return c.streamClient.Do(req)
}

type bufferedReadCloser struct {
	*bufio.Reader
	io.Closer
}

func validateStreamResponse(resp *http.Response, method string) (*http.Response, error) {
	if resp == nil {
		return nil, fmt.Errorf("Jellyfin stream returned no response")
	}
	if resp.Body == nil {
		return nil, fmt.Errorf("Jellyfin stream returned status %d with no body", resp.StatusCode)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("Jellyfin stream returned status %d: %s", resp.StatusCode, detail)
	}
	if method == http.MethodHead {
		return resp, nil
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.Peek(1); err != nil {
		_ = resp.Body.Close()
		if err == io.EOF {
			return nil, fmt.Errorf("Jellyfin stream returned an empty response")
		}
		return nil, fmt.Errorf("read Jellyfin stream response: %w", err)
	}
	resp.Body = &bufferedReadCloser{Reader: reader, Closer: resp.Body}
	return resp, nil
}

func retryableStaleMediaSourceResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusNotFound ||
		(resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices)
}

// ReportPlayback updates Jellyfin's active-session dashboard for a direct-play item.
func (c *Client) ReportPlayback(ctx context.Context, serverURL, token, event string, report PlaybackReport) error {
	path := "/Sessions/Playing/Progress"
	switch event {
	case "start":
		path = "/Sessions/Playing"
	case "stop":
		path = "/Sessions/Playing/Stopped"
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(token))
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Jellyfin playback report failed: %s", resp.Status)
	}
	return nil
}

func (c *Client) OpenImage(ctx context.Context, serverURL, token, itemID, kind string) (*http.Response, error) {
	if kind == "" {
		kind = "Primary"
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/Items/" + url.PathEscape(itemID) + "/Images/" + url.PathEscape(kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(token))
	return c.httpClient.Do(req)
}

func (c *Client) getJSON(serverURL, token, path string, params url.Values, out interface{}) error {
	endpoint := strings.TrimRight(serverURL, "/") + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(token))
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetFavorites fetches favorited movies and series from Jellyfin.
func (c *Client) GetFavorites(serverURL, token, userID string) ([]JellyfinItem, error) {
	serverURL = strings.TrimRight(serverURL, "/")

	params := url.Values{
		"Filters":          {"IsFavorite"},
		"IncludeItemTypes": {"Movie,Series"},
		"Recursive":        {"true"},
		"Fields":           {"ProviderIds"},
	}

	endpoint := fmt.Sprintf("%s/Users/%s/Items?%s", serverURL, userID, params.Encode())

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch favorites: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch favorites failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Items []JellyfinItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode favorites: %w", err)
	}

	// Normalize provider ID keys to lowercase
	for i := range result.Items {
		result.Items[i].ProviderIDs = normalizeProviderIDs(result.Items[i].ProviderIDs)
	}

	return result.Items, nil
}

// GetWatchHistory fetches played movies, series, and episodes from Jellyfin.
func (c *Client) GetWatchHistory(serverURL, token, userID string) ([]JellyfinItem, error) {
	serverURL = strings.TrimRight(serverURL, "/")

	params := url.Values{
		"Filters":          {"IsPlayed"},
		"IncludeItemTypes": {"Movie,Series,Episode"},
		"Recursive":        {"true"},
		"Fields":           {"ProviderIds"},
		"SortBy":           {"DatePlayed"},
		"SortOrder":        {"Descending"},
	}

	endpoint := fmt.Sprintf("%s/Users/%s/Items?%s", serverURL, userID, params.Encode())

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch watch history: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch watch history failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Items []JellyfinItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode watch history: %w", err)
	}

	// Normalize provider ID keys to lowercase
	for i := range result.Items {
		result.Items[i].ProviderIDs = normalizeProviderIDs(result.Items[i].ProviderIDs)
	}

	return result.Items, nil
}

// normalizeProviderIDs converts provider ID keys to lowercase (Tmdb → tmdb, Imdb → imdb).
func normalizeProviderIDs(ids map[string]string) map[string]string {
	if ids == nil {
		return nil
	}
	normalized := make(map[string]string, len(ids))
	for k, v := range ids {
		normalized[strings.ToLower(k)] = v
	}
	return normalized
}

// NormalizeMediaType converts Jellyfin types to our internal format.
func NormalizeMediaType(jellyfinType string) string {
	switch jellyfinType {
	case "Movie":
		return "movie"
	case "Series":
		return "series"
	case "Episode":
		return "episode"
	default:
		return strings.ToLower(jellyfinType)
	}
}
