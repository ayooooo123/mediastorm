package scrob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client { return &Client{httpClient: &http.Client{Timeout: defaultTimeout}} }

func NewClientWithHTTPClient(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{httpClient: client}
}

type HistoryResponse struct {
	Page         int            `json:"page"`
	PageSize     int            `json:"page_size"`
	TotalResults int            `json:"total_results"`
	TotalPages   int            `json:"total_pages"`
	Results      []HistoryEvent `json:"results"`
}

type HistoryEvent struct {
	ID        int        `json:"id"`
	WatchedAt *time.Time `json:"watched_at"`
	Completed bool       `json:"completed"`
	Media     Media      `json:"media"`
}

// UnmarshalJSON accepts both RFC3339 timestamps and Scrob's timezone-less UTC
// timestamps (for example, "2026-08-08T18:49:42.123456").
func (e *HistoryEvent) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        int     `json:"id"`
		WatchedAt *string `json:"watched_at"`
		Completed bool    `json:"completed"`
		Media     Media   `json:"media"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.ID, e.Completed, e.Media = raw.ID, raw.Completed, raw.Media
	e.WatchedAt = nil
	if raw.WatchedAt == nil || strings.TrimSpace(*raw.WatchedAt) == "" {
		return nil
	}
	value := strings.TrimSpace(*raw.WatchedAt)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			parsed = parsed.UTC()
			e.WatchedAt = &parsed
			return nil
		}
	}
	return fmt.Errorf("invalid Scrob watched_at timestamp %q", value)
}

type Media struct {
	ID            int    `json:"id"`
	TMDBID        int    `json:"tmdb_id"`
	Type          string `json:"type"`
	Title         string `json:"title"`
	ReleaseDate   string `json:"release_date"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	ShowTitle     string `json:"show_title"`
	ShowTMDBID    int    `json:"show_tmdb_id"`
	ShowTVDBID    int    `json:"show_tvdb_id"`
}

type WatchEvent struct {
	TMDBID        int        `json:"tmdb_id"`
	MediaType     string     `json:"media_type"`
	WatchedAt     *time.Time `json:"watched_at,omitempty"`
	Completed     bool       `json:"completed"`
	SeriesTMDBID  int        `json:"series_tmdb_id,omitempty"`
	SeriesTVDBID  int        `json:"series_tvdb_id,omitempty"`
	SeasonNumber  int        `json:"season_number"`
	EpisodeNumber int        `json:"episode_number"`
}

type ManualSessionStart struct {
	TMDBID        int    `json:"tmdb_id,omitempty"`
	MediaType     string `json:"media_type"`
	Title         string `json:"title,omitempty"`
	Runtime       int    `json:"runtime,omitempty"`
	ShowTMDBID    int    `json:"show_tmdb_id,omitempty"`
	SeasonNumber  *int   `json:"season_number,omitempty"`
	EpisodeNumber *int   `json:"episode_number,omitempty"`
}

type ManualSessionUpdate struct {
	ProgressSeconds int    `json:"progress_seconds"`
	State           string `json:"state,omitempty"`
}

type ManualSessionResponse struct {
	SessionKey string `json:"session_key"`
	MediaID    int    `json:"media_id"`
	Runtime    int    `json:"runtime"`
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("Scrob URL must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Scrob URL must not contain credentials, a query, or a fragment")
	}
	if !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/api/proxy") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/proxy"
		raw = strings.TrimRight(parsed.String(), "/")
	}
	return raw, nil
}

func (c *Client) GetHistory(ctx context.Context, baseURL, apiKey string) ([]HistoryEvent, error) {
	baseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("Scrob API key is required")
	}

	var all []HistoryEvent
	for page := 1; ; page++ {
		u := fmt.Sprintf("%s/history?page=%d&page_size=100", baseURL, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Api-Key", apiKey)
		req.Header.Set("Accept", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch Scrob history page %d: %w", page, err)
		}
		var payload HistoryResponse
		err = decodeResponse(resp, &payload)
		if err != nil {
			return nil, fmt.Errorf("fetch Scrob history page %d: %w", page, err)
		}
		all = append(all, payload.Results...)
		if page >= payload.TotalPages || len(payload.Results) == 0 {
			break
		}
	}
	return all, nil
}

// TestConnection validates the instance URL and API key without downloading a
// user's complete history.
func (c *Client) TestConnection(ctx context.Context, baseURL, apiKey string) error {
	baseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("Scrob API key is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/history?page=1&page_size=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to Scrob: %w", err)
	}
	var payload HistoryResponse
	if err := decodeResponse(resp, &payload); err != nil {
		return fmt.Errorf("connect to Scrob: %w", err)
	}
	return nil
}

func (c *Client) Login(ctx context.Context, baseURL, apiKey, username, password, twoFactorCode string) (string, error) {
	baseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return "", errors.New("Scrob username and password are required for outbound sync")
	}
	form := url.Values{"username": {username}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The public Scrob frontend proxy uses the API key to let this request reach
	// the backend before a JWT has been issued. The login endpoint itself still
	// validates username/password and ignores the extra key.
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("log in to Scrob: %w", err)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		Requires2FA bool   `json:"requires_2fa"`
		TempToken   string `json:"temp_token"`
	}
	if err := decodeResponse(resp, &payload); err != nil {
		return "", fmt.Errorf("log in to Scrob: %w", err)
	}
	if payload.Requires2FA {
		if strings.TrimSpace(twoFactorCode) == "" {
			return "", errors.New("Scrob account requires a 2FA code")
		}
		verifyBody := map[string]string{"temp_token": payload.TempToken, "code": strings.TrimSpace(twoFactorCode)}
		data, err := json.Marshal(verifyBody)
		if err != nil {
			return "", err
		}
		verifyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/2fa/verify-login", bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		verifyReq.Header.Set("Content-Type", "application/json")
		verifyReq.Header.Set("X-Api-Key", apiKey)
		verifyResp, err := c.httpClient.Do(verifyReq)
		if err != nil {
			return "", fmt.Errorf("verify Scrob 2FA: %w", err)
		}
		var verified struct {
			AccessToken string `json:"access_token"`
		}
		if err := decodeResponse(verifyResp, &verified); err != nil {
			return "", fmt.Errorf("verify Scrob 2FA: %w", err)
		}
		payload.AccessToken = verified.AccessToken
	}
	if payload.AccessToken == "" {
		return "", errors.New("Scrob login returned no access token")
	}
	return payload.AccessToken, nil
}

// GenerateTOTPCode generates Scrob's default six-digit, SHA-1, 30-second TOTP.
// It accepts either a raw Base32 seed or a standard otpauth:// URI.
func GenerateTOTPCode(secret string, now time.Time) (string, error) {
	secret = strings.TrimSpace(secret)
	if strings.HasPrefix(strings.ToLower(secret), "otpauth://") {
		u, err := url.Parse(secret)
		if err != nil {
			return "", errors.New("invalid Scrob TOTP URI")
		}
		secret = u.Query().Get("secret")
	}
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	secret = strings.TrimRight(secret, "=")
	if secret == "" {
		return "", errors.New("Scrob TOTP secret is empty")
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return "", errors.New("invalid Scrob TOTP secret")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, decoded)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

func (c *Client) AddHistory(ctx context.Context, baseURL, apiKey, token string, event WatchEvent) error {
	return c.doJSON(ctx, http.MethodPost, baseURL, "/history", apiKey, token, event)
}

func (c *Client) RemoveHistory(ctx context.Context, baseURL, apiKey, token string, tmdbID int, mediaType string) error {
	path := "/history/item?tmdb_id=" + strconv.Itoa(tmdbID) + "&media_type=" + url.QueryEscape(mediaType)
	return c.doJSON(ctx, http.MethodDelete, baseURL, path, apiKey, token, nil)
}

func (c *Client) StartSession(ctx context.Context, baseURL, apiKey, token string, session ManualSessionStart) (ManualSessionResponse, error) {
	var response ManualSessionResponse
	err := c.doJSONResponse(ctx, http.MethodPost, baseURL, "/history/session/start", apiKey, token, session, &response)
	if err == nil && response.SessionKey == "" {
		err = errors.New("Scrob session start returned no session key")
	}
	return response, err
}

func (c *Client) UpdateSession(ctx context.Context, baseURL, apiKey, token, sessionKey string, update ManualSessionUpdate) error {
	path := "/history/session/" + url.PathEscape(sessionKey)
	return c.doJSON(ctx, http.MethodPatch, baseURL, path, apiKey, token, update)
}

func (c *Client) StopSession(ctx context.Context, baseURL, apiKey, token, sessionKey string) error {
	path := "/history/session/" + url.PathEscape(sessionKey)
	return c.doJSON(ctx, http.MethodDelete, baseURL, path, apiKey, token, nil)
}

func (c *Client) doJSON(ctx context.Context, method, baseURL, path, apiKey, token string, body any) error {
	return c.doJSONResponse(ctx, method, baseURL, path, apiKey, token, body, nil)
}

func (c *Client) doJSONResponse(ctx context.Context, method, baseURL, path, apiKey, token string, body, response any) error {
	baseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Scrob %s %s: %w", method, path, err)
	}
	return decodeResponse(resp, response)
}

func decodeResponse(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(limited)))
	}
	if dst == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
