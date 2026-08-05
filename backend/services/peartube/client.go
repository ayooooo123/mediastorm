// Package peartube integrates a PearTube relay as a peer-to-peer media source.
//
// A relay is a companion process that speaks a small machine API over plain
// HTTP: GET /api/v1/catalog lists what the swarm can serve, GET
// /api/v1/stream/{publicationId}/{renditionId} serves a rendition's bytes with
// byte-range support, and POST /api/v1/archive publishes media into the swarm —
// either as an uploaded file, or as a URL the relay fetches for itself.
// Viewers that seed become the CDN for the next viewer.
//
// Trust model: the relay's HTTP surface is unauthenticated, matching the
// operator console it is mounted beside. It is expected to be reachable only
// from this backend (loopback by default). Nothing here invents credentials,
// and nothing here is active unless a relay URL is configured.
package peartube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
)

const (
	// RelayURLEnv names the relay base URL. Its absence, with nothing stored in
	// the admin settings either, is what keeps the whole integration inert for
	// installs that never asked for it.
	RelayURLEnv = "PEARTUBE_RELAY_URL"
	// EnabledEnv force-enables (with the default URL) or force-disables the
	// integration independently of the URL.
	EnabledEnv = "PEARTUBE_ENABLED"
	// DefaultRelayURL is where `peartube relay` listens out of the box: the
	// archive UI host and port default to 127.0.0.1:8174
	// (peartube packages/cli/src/constants.js), and the JSON API is served on
	// the same socket under /api/v1.
	DefaultRelayURL = "http://127.0.0.1:8174"

	apiPrefix = "/api/v1"

	// The relay bounds a catalog page at 50 entities and answers a catalog read
	// within its own 10s deadline.
	catalogPageLimit = 50
	catalogMaxPages  = 20
	catalogTTL       = 30 * time.Second

	requestTimeout = 20 * time.Second
)

var (
	// defaultMu guards the process-wide client. Everything that can seed,
	// search, resolve, or proxy p2p media reads it, and the admin settings page
	// can replace it mid-flight, so it is a lock rather than a sync.Once.
	defaultMu     sync.RWMutex
	defaultClient *Client
	// defaultConfigured records that a configuration has been applied, so the
	// first reader falls back to the environment exactly once instead of
	// re-reading it on every call.
	defaultConfigured bool
)

// Default returns the process-wide relay client, or nil when no relay is
// configured. Every integration point treats nil as "this feature does not
// exist", so an install with no relay behaves exactly as before.
//
// Read it per use rather than capturing it: Configure can replace it when an
// operator saves the admin settings, and a captured client goes stale.
func Default() *Client {
	defaultMu.RLock()
	if defaultConfigured {
		client := defaultClient
		defaultMu.RUnlock()
		return client
	}
	defaultMu.RUnlock()

	defaultMu.Lock()
	defer defaultMu.Unlock()
	if !defaultConfigured {
		// Reached before the admin settings were applied — during startup, or
		// in a test. The environment alone is the answer, which is what this
		// integration did before it was configurable.
		applyLocked(resolve(config.PearTubeSettings{}, os.Getenv))
	}
	return defaultClient
}

// Configure installs an effective configuration process-wide and returns the
// resulting client, which is nil when no relay is configured.
//
// It is called once at startup with the stored settings and again every time
// they are saved, which is what lets an operator point this backend at a relay,
// move it, or switch it off without restarting the container.
func Configure(resolved Resolved) *Client {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	applyLocked(resolved)
	return defaultClient
}

// applyLocked installs a configuration. Must be called with defaultMu held.
//
// An unchanged URL keeps the existing client rather than building an equivalent
// one, so a settings save unrelated to p2p does not throw away the catalog cache
// or re-announce a relay gate that never moved.
func applyLocked(resolved Resolved) {
	defaultConfigured = true

	previous := ""
	if defaultClient != nil {
		previous = defaultClient.baseURL
	}
	if resolved.RelayURL == "" {
		if previous != "" {
			log.Printf("[peartube] relay disabled (was %s)", previous)
		}
		defaultClient = nil
		return
	}
	client, err := New(resolved.RelayURL)
	if err != nil {
		log.Printf("[peartube] relay disabled: %v", err)
		defaultClient = nil
		return
	}
	if client.baseURL == previous {
		return
	}
	log.Printf("[peartube] relay configured at %s", client.baseURL)
	defaultClient = client
}

// New builds a relay client for an explicit base URL.
//
// The errors name the value rather than where it came from: a relay URL now
// arrives from the admin settings as often as from PEARTUBE_RELAY_URL, and an
// operator typing one into a settings field must not be told to go fix an
// environment variable they never set.
func New(rawBaseURL string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return nil, fmt.Errorf("relay URL %q is not a URL: %w", rawBaseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("relay URL %q must be an http(s) URL", rawBaseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("relay URL %q is missing a host", rawBaseURL)
	}
	base := strings.TrimSuffix(parsed.Scheme+"://"+parsed.Host+parsed.Path, "/")
	return &Client{
		baseURL: base,
		http:    &http.Client{Timeout: requestTimeout},
		// An archive upload streams a whole media file; a wall-clock deadline
		// would abort large seeds partway through.
		uploads: &http.Client{},
	}, nil
}

// Client talks to one PearTube relay.
type Client struct {
	baseURL string
	http    *http.Client
	uploads *http.Client

	mu          sync.Mutex
	cached      []CatalogEntity
	cachedAt    time.Time
	cachedError error
	// gateNoted is the last open-access state reported to the log, so the gate
	// is announced on transition instead of on every search.
	gateNoted bool
}

// BaseURL returns the relay origin, normalized without a trailing slash.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// StreamURL is the range-capable playback URL for one published rendition.
func (c *Client) StreamURL(publicationID, renditionID string) string {
	if c == nil {
		return ""
	}
	return c.baseURL + apiPrefix + "/stream/" + url.PathEscape(publicationID) + "/" + url.PathEscape(renditionID)
}

// OwnsURL reports whether a URL addresses this relay. Playback resolution uses
// it so a p2p result can never redirect the server at an arbitrary origin.
func (c *Client) OwnsURL(raw string) bool {
	if c == nil {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == base.Scheme && strings.EqualFold(parsed.Host, base.Host)
}

// CatalogSource is one publisher's copy of a rendition.
type CatalogSource struct {
	PublicationID string `json:"publicationId"`
	PublisherID   string `json:"publisherId"`
	RenditionID   string `json:"renditionId"`
	CoreKey       string `json:"coreKey"`
	CoreLength    int64  `json:"coreLength"`
	ByteLength    int64  `json:"byteLength"`
	ContentKind   string `json:"contentKind"`
	MediaProvider string `json:"mediaProvider"`
	MediaID       string `json:"mediaId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
}

// CatalogEntity is one title (a movie, or a single episode) the swarm can serve.
type CatalogEntity struct {
	EntityID   string          `json:"entityId"`
	EntityKind string          `json:"entityKind"`
	Title      string          `json:"title"`
	Year       int             `json:"year"`
	Sources    []CatalogSource `json:"sources"`
}

type catalogPage struct {
	Entities   []CatalogEntity `json:"entities"`
	NextCursor string          `json:"nextCursor"`
}

// ArchiveJob is the relay's 202 answer to a seed submission.
type ArchiveJob struct {
	JobID      string `json:"jobId"`
	Status     string `json:"status"`
	EntityHint string `json:"entityHint"`
}

// ArchiveSource identifies the publication a finished seed produced.
type ArchiveSource struct {
	EntityID      string `json:"entityId"`
	PublicationID string `json:"publicationId"`
	ManifestID    string `json:"manifestId"`
	PublisherID   string `json:"publisherId"`
	RenditionID   string `json:"renditionId"`
	CoreKey       string `json:"coreKey"`
	CoreLength    int64  `json:"coreLength"`
	ByteLength    int64  `json:"byteLength"`
}

// ArchiveStatus is the relay's answer for a seed job in progress or finished.
type ArchiveStatus struct {
	JobID  string         `json:"jobId"`
	Status string         `json:"status"`
	Title  string         `json:"title"`
	Error  string         `json:"error"`
	Source *ArchiveSource `json:"source"`
}

// APIError carries the relay's structured error body.
type APIError struct {
	Status  int
	Code    string
	Message string
	Field   string
}

func (e *APIError) Error() string {
	parts := make([]string, 0, 3)
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Field != "" {
		parts = append(parts, "field="+e.Field)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("relay returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("relay returned HTTP %d: %s", e.Status, strings.Join(parts, ": "))
}

// The relay gates enumeration and byte serving on how it is bound. Bound to
// loopback it answers freely; bound to 0.0.0.0 or any other interface it
// refuses GET /api/v1/catalog and GET /api/v1/stream until the operator opts
// in. Seeding (POST /api/v1/archive) is never gated.
//
// This backend usually runs in a container and therefore reaches the relay over
// a non-loopback address, so the gate is the expected first-run state rather
// than a malfunction. It has to be named as such, or an unconfigured relay
// looks like a broken integration.
const (
	// openAccessNotEnabledCode is the relay's error code for that refusal. Only
	// the code is stable: the message embeds the actual bind address.
	openAccessNotEnabledCode = "OPEN_ACCESS_NOT_ENABLED"

	// NotOpenRemedy is the operator action that clears the gate, worded so it
	// can be shown to a person verbatim.
	NotOpenRemedy = "restart the relay with --api-open (or PEARTUBE_ARCHIVE_API_OPEN=1)"
)

// ErrRelayNotOpen marks a relay that is up and answering but refusing to
// enumerate or serve media because open access was never enabled.
//
// It is deliberately distinct from "the relay is unreachable" and from "the
// relay has nothing matching": this one is cleared by an operator, not by
// retrying or by searching for something else.
var ErrRelayNotOpen = errors.New("peartube relay will not enumerate or serve media until open access is enabled: " + NotOpenRemedy)

// sourceRefusedPrefix is the shared prefix of every relay error code that means
// "the URL you gave me is not one I will fetch": a bad scheme, embedded
// credentials, a host that is not publicly routable, a name that will not
// resolve, or a missing/ambiguous source. They are all the caller's problem, so
// they must be distinguishable from a relay that is merely broken or down.
const sourceRefusedPrefix = "SOURCE_"

// ErrSourceRefused marks a URL seed the relay declined to fetch because of the
// URL itself.
//
// Nothing about the relay needs fixing when this happens, and retrying the same
// URL fails identically: the caller has to supply a different one. The specific
// reason stays on the APIError's Code.
var ErrSourceRefused = errors.New("peartube relay refused the source URL")

// IsSourceRefused reports whether the relay rejected the seed's source URL.
func IsSourceRefused(err error) bool {
	return errors.Is(err, ErrSourceRefused)
}

// Unwrap lets errors.Is see the relay's actionable refusals — the open-access
// gate, and a source URL the relay will not fetch — through the structured
// error, so callers match on a sentinel instead of re-deriving an error code.
func (e *APIError) Unwrap() error {
	switch {
	case e.Code == openAccessNotEnabledCode:
		return ErrRelayNotOpen
	case strings.HasPrefix(e.Code, sourceRefusedPrefix):
		return ErrSourceRefused
	}
	return nil
}

// IsRelayNotOpen reports whether err is the relay's open-access gate.
func IsRelayNotOpen(err error) bool {
	return errors.Is(err, ErrRelayNotOpen)
}

// noteGate reports a change in the relay's open-access state, and says nothing
// while it holds steady. Must be called with c.mu held.
//
// A stream search runs on every play attempt, so logging the gate per search
// would bury the log in one repeated line. Logging only on transition means an
// operator sees it once when it starts, and once more when it clears.
func (c *Client) noteGate(err error) {
	gated := IsRelayNotOpen(err)
	if gated == c.gateNoted {
		return
	}
	c.gateNoted = gated
	if !gated {
		log.Printf("[peartube] relay %s is serving media again", c.baseURL)
		return
	}
	detail := gateDetail(err)
	// The relay's own message usually ends in the same remedy; only append it
	// when the relay did not already say it.
	if !strings.Contains(detail, NotOpenRemedy) {
		detail += " -- " + NotOpenRemedy
	}
	log.Printf("[peartube] WARN: relay %s is reachable but refuses to enumerate or serve media: %s",
		c.baseURL, detail)
}

// gateDetail is the relay's own explanation, which names the address it is
// bound to. It falls back to the remedy when the relay sent no message.
func gateDetail(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if message := strings.TrimSpace(apiErr.Message); message != "" {
			return message
		}
	}
	return NotOpenRemedy
}

// RelayState is a plain verdict on what the relay can currently do, for the
// p2p status endpoint. "Reachable but not open" is its own answer: the relay is
// up and still accepts seeds, it just will not enumerate or serve media yet.
type RelayState struct {
	RelayURL  string `json:"relayUrl"`
	Reachable bool   `json:"reachable"`
	// NotOpen is the operator-fixable state: the relay answered with its
	// open-access refusal. Remedy says what to do about it.
	NotOpen bool `json:"notOpen"`
	// SeedingAvailable records that POST /api/v1/archive is not gated, so a
	// relay that refuses to enumerate can still be seeded to.
	SeedingAvailable bool   `json:"seedingAvailable"`
	CatalogEntities  int    `json:"catalogEntities"`
	Remedy           string `json:"remedy,omitempty"`
	Detail           string `json:"detail,omitempty"`
}

// Probe asks the relay what it can currently do, reusing the catalog cache so
// a status poll costs nothing when a search just ran.
func (c *Client) Probe(ctx context.Context) RelayState {
	if c == nil {
		return RelayState{}
	}
	state := RelayState{RelayURL: c.baseURL}
	entities, err := c.Catalog(ctx)
	switch {
	case err == nil:
		state.Reachable = true
		state.CatalogEntities = len(entities)
	case IsRelayNotOpen(err):
		state.Reachable = true
		state.NotOpen = true
		state.Remedy = NotOpenRemedy
		state.Detail = gateDetail(err)
	default:
		// An APIError means the relay answered, just not with a catalog; a
		// transport error means nothing answered at all.
		var apiErr *APIError
		state.Reachable = errors.As(err, &apiErr)
		state.Detail = err.Error()
	}
	// Seeding is only gated by whether the relay is there to accept it.
	state.SeedingAvailable = state.Reachable
	return state
}

// Catalog returns every entity the relay can serve, cached briefly so a burst
// of stream searches does not re-walk the relay's publisher catalogs.
func (c *Client) Catalog(ctx context.Context) ([]CatalogEntity, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.cachedAt) < catalogTTL {
		return c.cached, c.cachedError
	}
	entities, err := c.fetchCatalog(ctx)
	// A cancelled request says nothing about the relay, so it must not poison
	// the cache for the next caller.
	if errors.Is(err, context.Canceled) {
		return nil, err
	}
	c.cached, c.cachedError, c.cachedAt = entities, err, time.Now()
	c.noteGate(err)
	return entities, err
}

func (c *Client) fetchCatalog(ctx context.Context) ([]CatalogEntity, error) {
	var (
		entities []CatalogEntity
		cursor   string
	)
	for range catalogMaxPages {
		query := url.Values{"limit": {strconv.Itoa(catalogPageLimit)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var decoded catalogPage
		if err := c.getJSON(ctx, apiPrefix+"/catalog?"+query.Encode(), &decoded); err != nil {
			return nil, err
		}
		entities = append(entities, decoded.Entities...)
		if decoded.NextCursor == "" {
			return entities, nil
		}
		cursor = decoded.NextCursor
	}
	log.Printf("[peartube] catalog walk stopped at %d pages (%d entities)", catalogMaxPages, len(entities))
	return entities, nil
}

// ArchiveStatus polls one seed job.
func (c *Client) ArchiveStatus(ctx context.Context, jobID string) (*ArchiveStatus, error) {
	if c == nil {
		return nil, errors.New("peartube relay is not configured")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	var status ArchiveStatus
	if err := c.getJSON(ctx, apiPrefix+"/archive/"+url.PathEscape(jobID), &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// ArchiveCoordinates are the TMDB coordinates a seed is published under. Both
// seed transports carry exactly this set; only where the bytes come from
// differs, so the coordinates live in one place.
type ArchiveCoordinates struct {
	ContentKind string // "movie" or "episode"
	TMDBID      string
	TMDBTitle   string
	TMDBYear    int
	TMDBSeason  int
	TMDBEpisode int
	PosterPath  string
	Overview    string
	Runtime     int
	Genres      string
}

// Validate rejects coordinates the relay would reject, so an obvious mistake
// costs no round trip. It is exported because the caller that assembled the
// coordinates is the one that can report the problem as a client error rather
// than as a relay failure.
func (c ArchiveCoordinates) Validate() error {
	switch c.ContentKind {
	case "movie":
		if c.TMDBSeason != 0 || c.TMDBEpisode != 0 {
			return errors.New("a movie cannot carry season or episode coordinates")
		}
	case "episode":
		if c.TMDBSeason < 1 || c.TMDBEpisode < 1 {
			return errors.New("an episode requires tmdbSeason and tmdbEpisode")
		}
	default:
		return fmt.Errorf("contentKind must be movie or episode, got %q", c.ContentKind)
	}
	if strings.TrimSpace(c.TMDBID) == "" {
		return errors.New("tmdbId is required")
	}
	if strings.TrimSpace(c.TMDBTitle) == "" {
		return errors.New("tmdbTitle is required")
	}
	return nil
}

// ArchiveRequest describes a local file to publish into the swarm. The relay
// receives the bytes.
type ArchiveRequest struct {
	FilePath       string
	IdempotencyKey string
	ArchiveCoordinates
}

func (r ArchiveRequest) Validate() error {
	if strings.TrimSpace(r.FilePath) == "" {
		return errors.New("filePath is required")
	}
	return r.ArchiveCoordinates.Validate()
}

// ArchiveURLRequest describes a URL the relay fetches for itself. This is what
// makes a debrid or usenet stream seedable: this backend never holds those
// bytes, so it hands over an address instead of a body.
type ArchiveURLRequest struct {
	SourceURL      string
	IdempotencyKey string
	ArchiveCoordinates
}

func (r ArchiveURLRequest) Validate() error {
	// Only the structural checks live here. Whether the host is publicly
	// routable, whether the scheme is allowed, and whether embedded credentials
	// disqualify the URL are the relay's calls, and its refusal codes are more
	// specific than anything this side could re-derive.
	trimmed := strings.TrimSpace(r.SourceURL)
	if trimmed == "" {
		return errors.New("url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("url is not a valid URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("url must be absolute, with a scheme and a host")
	}
	return r.ArchiveCoordinates.Validate()
}

// Archive uploads a file to the relay for publication. The body is streamed
// from disk, never buffered: these are whole movies.
func (c *Client) Archive(ctx context.Context, req ArchiveRequest) (*ArchiveJob, error) {
	if c == nil {
		return nil, errors.New("peartube relay is not configured")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	file, err := os.Open(req.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open media file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat media file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", req.FilePath)
	}

	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		writer.CloseWithError(writeArchiveForm(form, file, filepath.Base(req.FilePath), req.ArchiveCoordinates))
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+apiPrefix+"/archive", reader)
	if err != nil {
		reader.CloseWithError(err)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", form.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		httpReq.Header.Set("Idempotency-Key", key)
	}

	resp, err := c.uploads.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("archive upload: %w", err)
	}
	defer resp.Body.Close()
	var job ArchiveJob
	if err := decodeResponse(resp, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// archiveURLBody is the relay's JSON seed contract. The relay rejects any field
// it does not know, and requires season/episode to be absent for a movie, so
// every optional field is omitted rather than zeroed.
type archiveURLBody struct {
	URL            string `json:"url"`
	ContentKind    string `json:"contentKind"`
	TMDBID         string `json:"tmdbId"`
	TMDBTitle      string `json:"tmdbTitle"`
	TMDBSeason     int    `json:"tmdbSeason,omitempty"`
	TMDBEpisode    int    `json:"tmdbEpisode,omitempty"`
	TMDBYear       string `json:"tmdbYear,omitempty"`
	TMDBPosterPath string `json:"tmdbPosterPath,omitempty"`
	TMDBOverview   string `json:"tmdbOverview,omitempty"`
	TMDBRuntime    string `json:"tmdbRuntime,omitempty"`
	TMDBGenres     string `json:"tmdbGenres,omitempty"`
}

// ArchiveURL asks the relay to fetch a URL itself and publish what it finds.
//
// This is the seed path for content this backend only ever proxies: a debrid or
// usenet stream lives on someone else's CDN, so there is no file to upload. No
// media bytes cross this process.
//
// A URL the relay will not fetch comes back as an *APIError that satisfies
// IsSourceRefused, which is a different problem from the relay being down.
func (c *Client) ArchiveURL(ctx context.Context, req ArchiveURLRequest) (*ArchiveJob, error) {
	if c == nil {
		return nil, errors.New("peartube relay is not configured")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	body := archiveURLBody{
		URL:            strings.TrimSpace(req.SourceURL),
		ContentKind:    req.ContentKind,
		TMDBID:         strings.TrimSpace(req.TMDBID),
		TMDBTitle:      strings.TrimSpace(req.TMDBTitle),
		TMDBPosterPath: strings.TrimSpace(req.PosterPath),
		TMDBOverview:   strings.TrimSpace(req.Overview),
		TMDBGenres:     strings.TrimSpace(req.Genres),
	}
	if req.ContentKind == "episode" {
		body.TMDBSeason = req.TMDBSeason
		body.TMDBEpisode = req.TMDBEpisode
	}
	if req.TMDBYear > 0 {
		body.TMDBYear = strconv.Itoa(req.TMDBYear)
	}
	if req.Runtime > 0 {
		body.TMDBRuntime = strconv.Itoa(req.Runtime)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode archive request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+apiPrefix+"/archive", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		httpReq.Header.Set("Idempotency-Key", key)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("archive by url: %w", err)
	}
	defer resp.Body.Close()
	var job ArchiveJob
	if err := decodeResponse(resp, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func writeArchiveForm(form *multipart.Writer, file io.Reader, fileName string, req ArchiveCoordinates) error {
	fields := [][2]string{
		{"contentKind", req.ContentKind},
		{"tmdbId", req.TMDBID},
		{"tmdbTitle", req.TMDBTitle},
		{"tmdbPosterPath", req.PosterPath},
		{"tmdbOverview", req.Overview},
		{"tmdbGenres", req.Genres},
	}
	if req.TMDBYear > 0 {
		fields = append(fields, [2]string{"tmdbYear", strconv.Itoa(req.TMDBYear)})
	}
	if req.Runtime > 0 {
		fields = append(fields, [2]string{"tmdbRuntime", strconv.Itoa(req.Runtime)})
	}
	if req.ContentKind == "episode" {
		fields = append(fields,
			[2]string{"tmdbSeason", strconv.Itoa(req.TMDBSeason)},
			[2]string{"tmdbEpisode", strconv.Itoa(req.TMDBEpisode)},
		)
	}
	for _, field := range fields {
		if field[1] == "" {
			continue
		}
		if err := form.WriteField(field[0], field[1]); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("file", fileName)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	return form.Close()
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeResponse(resp, out)
}

// maxErrorBody bounds what we read from a failing relay before giving up on
// finding a structured error in it.
const maxErrorBody = 64 << 10

func decodeResponse(resp *http.Response, out any) error {
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		apiErr := &APIError{Status: resp.StatusCode}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Field   string `json:"field"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			apiErr.Code = envelope.Error.Code
			apiErr.Message = envelope.Error.Message
			apiErr.Field = envelope.Error.Field
		}
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(body))
		}
		return apiErr
	}
	if out == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode relay response: %w", err)
	}
	return nil
}
