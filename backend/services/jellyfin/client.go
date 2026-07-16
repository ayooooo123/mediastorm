package jellyfin

import (
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
	Overview          string                `json:"Overview,omitempty"`
	OfficialRating    string                `json:"OfficialRating,omitempty"`
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
	libraries := make([]JellyfinLibrary, 0, len(result.Items))
	for _, item := range result.Items {
		switch strings.ToLower(item.CollectionType) {
		case "movies", "tvshows":
			libraries = append(libraries, item)
		}
	}
	return libraries, nil
}

func (c *Client) GetLibraryItems(serverURL, token, userID, libraryID, collectionType string) ([]JellyfinItem, error) {
	params := url.Values{
		"ParentId": {libraryID}, "Recursive": {"true"},
		"Fields":       {"ProviderIds,Overview,OfficialRating,MediaSources,MediaStreams,ImageTags,BackdropImageTags"},
		"EnableImages": {"true"}, "EnableTotalRecordCount": {"true"},
	}
	if strings.EqualFold(collectionType, "tvshows") {
		params.Set("IncludeItemTypes", "Episode")
	} else {
		params.Set("IncludeItemTypes", "Movie")
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
