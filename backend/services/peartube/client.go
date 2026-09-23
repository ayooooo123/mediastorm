// Package peartube is a client for the PearTube relay /v1 API.
package peartube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	requestTimeout   = 20 * time.Second
	maxResponseBytes = 4 << 20
)

// Job statuses reported by the relay.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Result is one tracker entry for an id. StreamURL is an absolute,
// Range-capable URL on the relay's blob server that needs no extra auth.
type Result struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Local     bool   `json:"local"`
	StreamURL string `json:"streamUrl"`
}

// Job is an acquire job. The relay never echoes its source back.
type Job struct {
	JobID   string `json:"jobId"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Created int64  `json:"created"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Active reports whether the relay is still working on the job.
func (j Job) Active() bool {
	return j.Status == JobQueued || j.Status == JobRunning
}

// Status describes the relay.
type Status struct {
	Tracker   string `json:"tracker"`
	Writer    string `json:"writer"`
	Blobs     string `json:"blobs"`
	BlobBytes int64  `json:"blobBytes"`
	Peers     int    `json:"peers"`
}

// APIError is a non-2xx relay answer.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("peartube relay: %d %s", e.StatusCode, e.Message)
}

// Client talks to one relay.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

// New builds a client for the relay at baseURL, authenticated with secret.
func New(baseURL, secret string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("relay URL must be an http(s) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("relay URL must not include credentials, a query, or a fragment")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("relay secret is required")
	}
	return &Client{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		secret:  secret,
		http:    &http.Client{Timeout: requestTimeout},
	}, nil
}

// BaseURL is the relay address this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// Search lists the tracker entries for id.
func (c *Client) Search(ctx context.Context, id string) ([]Result, error) {
	var out struct {
		Results []Result `json:"results"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/search?id="+url.QueryEscape(id), nil, &out)
	return out.Results, err
}

// Acquire asks the relay to fetch sourceURL (with headers), store it, and
// announce it under id.
func (c *Client) Acquire(ctx context.Context, id, title, sourceURL string, headers map[string]string) (Job, error) {
	type source struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers,omitempty"`
	}
	body := struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Source source `json:"source"`
	}{id, title, source{sourceURL, headers}}
	var job Job
	err := c.do(ctx, http.MethodPost, "/v1/acquire", body, &job)
	return job, err
}

// Job returns one acquire job.
func (c *Client) Job(ctx context.Context, jobID string) (Job, error) {
	var job Job
	err := c.do(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(jobID), nil, &job)
	return job, err
}

// Jobs lists every acquire job.
func (c *Client) Jobs(ctx context.Context) ([]Job, error) {
	var out struct {
		Jobs []Job `json:"jobs"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/jobs", nil, &out)
	return out.Jobs, err
}

// Cancel cancels an acquire job and returns its final state.
func (c *Client) Cancel(ctx context.Context, jobID string) (Job, error) {
	var job Job
	err := c.do(ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(jobID), nil, &job)
	return job, err
}

// Status describes the relay.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &status)
	return status, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("peartube relay: response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Error == "" {
			failure.Error = http.StatusText(resp.StatusCode)
		}
		return &APIError{StatusCode: resp.StatusCode, Message: failure.Error}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("peartube relay: decode %s %s: %w", method, strings.SplitN(path, "?", 2)[0], err)
	}
	return nil
}

var imdbIDPattern = regexp.MustCompile(`^tt\d+$`)

// MediaID builds the relay id for an IMDb title: `imdb:tt0111161` for a movie,
// `imdb:tt0944947:s01e02` for an episode. It returns "" when the inputs cannot
// form a valid id.
func MediaID(imdbID string, season, episode int) string {
	imdbID = strings.ToLower(strings.TrimSpace(imdbID))
	if !imdbIDPattern.MatchString(imdbID) {
		return ""
	}
	if season == 0 && episode == 0 {
		return "imdb:" + imdbID
	}
	if season < 0 || season > 99 || episode < 0 || episode > 999 {
		return ""
	}
	return fmt.Sprintf("imdb:%s:s%02de%02d", imdbID, season, episode)
}
