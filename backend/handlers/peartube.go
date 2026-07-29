package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/localmedia"
	"novastream/services/peartube"
)

// localMediaLibrary is the slice of the local media service the seed endpoint
// needs: resolve an item to a file on disk, and prove an explicit path lives
// inside a library the operator configured.
type localMediaLibrary interface {
	GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error)
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
}

var _ localMediaLibrary = (*localmedia.Service)(nil)

// streamURLResolver turns an internal stream path this backend handed the player
// (a /debrid/... path, most importantly) into the CDN URL it currently points
// at. debrid.CompositeProvider satisfies it via streaming.DirectURLProvider.
//
// Seeding needs it because the "resolved source URL" a debrid resolve produces
// is not always a URL: Torbox hands back an internal torrent_id:file_id
// reference that only becomes an address at stream time. Re-resolving here also
// sidesteps the short lifetime of the URLs that are addresses (see Seed).
type streamURLResolver interface {
	GetDirectURL(ctx context.Context, path string) (string, error)
}

// PearTubeHandler exposes the seeding half of the p2p integration: it publishes
// something this viewer can already play into the PearTube swarm, so the next
// viewer can stream it from the swarm instead of from a provider.
//
// Two kinds of source reach the relay, because this backend only sometimes holds
// the bytes. A local library item is uploaded from disk. Anything else — debrid,
// usenet, any resolved stream — is handed over as a URL that the relay fetches
// itself, so no media crosses this process.
//
// Seeding happens two ways. The frontend can ask for it explicitly, and — unless
// the operator turned it off — starting a playback asks for it automatically;
// see OnPlaybackStarted for why the trigger is a playback heartbeat and not a
// resolve or a byte range.
type PearTubeHandler struct {
	localMedia localMediaLibrary
	streams    streamURLResolver

	// configMu guards the effective configuration below, which the admin
	// settings page can replace while requests and heartbeats are in flight.
	configMu sync.RWMutex
	// relay is the client for the configured relay, or nil when this install has
	// no relay at all. Nil is the outer gate for the whole integration.
	relay *peartube.Client
	// autoSeed is the operator's switch for watch-triggered seeding.
	autoSeed bool
	// resolved is where the effective configuration came from, reported by
	// Status so the admin page can name the environment variable that is
	// supplying a value instead of presenting it as the operator's own choice.
	resolved peartube.Resolved
	// relayConsumers are the services that captured the relay client when they
	// were built and therefore have to be handed the new one on a settings save.
	relayConsumers []PearTubeRelayConsumer

	// autoSeedClaims holds the titles an automatic seed has already taken
	// responsibility for, keyed by peartube.EntityKey. See claimAutoSeed.
	autoSeedMu     sync.Mutex
	autoSeedClaims map[string]time.Time
}

// PearTubeRelayConsumer is a service that captured the relay client when it was
// built and cannot pick up a replacement on its own. indexer.Service satisfies
// it via SetPearTubeRelay.
type PearTubeRelayConsumer interface {
	SetPearTubeRelay(*peartube.Client)
}

// NewPearTubeHandler builds the handler with no relay. The effective
// configuration arrives from ApplyPearTubeSettings, which the caller must invoke
// once at startup with the stored settings.
func NewPearTubeHandler(localMedia *localmedia.Service) *PearTubeHandler {
	handler := &PearTubeHandler{}
	// A typed nil in an interface is not nil; only assign a real service.
	if localMedia != nil {
		handler.localMedia = localMedia
	}
	return handler
}

// AddRelayConsumer registers a service that has to be handed the relay client
// whenever it changes.
func (h *PearTubeHandler) AddRelayConsumer(consumer PearTubeRelayConsumer) {
	if consumer == nil {
		return
	}
	h.configMu.Lock()
	h.relayConsumers = append(h.relayConsumers, consumer)
	relay := h.relay
	h.configMu.Unlock()
	consumer.SetPearTubeRelay(relay)
}

// ApplyPearTubeSettings installs the operator's PearTube configuration on the
// running integration. It is called once at startup and again on every settings
// save, which is what lets an operator point this backend at a relay, move it,
// or switch it off without restarting the container.
//
// One call reconfigures everything: peartube.Configure replaces the process-wide
// client that playback resolution and the media proxy read per use, and the
// services that captured it at build time are handed the new one here.
func (h *PearTubeHandler) ApplyPearTubeSettings(stored config.PearTubeSettings) {
	resolved := peartube.Resolve(stored)
	relay := peartube.Configure(resolved)

	h.configMu.Lock()
	h.relay = relay
	h.autoSeed = resolved.AutoSeed
	h.resolved = resolved
	consumers := h.relayConsumers
	h.configMu.Unlock()

	for _, consumer := range consumers {
		consumer.SetPearTubeRelay(relay)
	}
}

// currentRelay returns the configured relay client, or nil when there is none.
func (h *PearTubeHandler) currentRelay() *peartube.Client {
	if h == nil {
		return nil
	}
	h.configMu.RLock()
	defer h.configMu.RUnlock()
	return h.relay
}

// SetStreamResolver supplies the streaming provider that resolves internal
// stream paths, enabling the streamPath form of a seed request. Without it, a
// caller can still seed by passing an already-resolved url.
func (h *PearTubeHandler) SetStreamResolver(streams streamURLResolver) {
	if streams != nil {
		h.streams = streams
	}
}

// SeedRequest names what to publish and the TMDB coordinates to publish it
// under. Exactly one source must be given:
//
//   - localMediaItemId — a local library item; the server resolves the path and
//     fills in the metadata the scanner matched.
//   - filePath — an explicit path, which must live inside a configured local
//     media library root.
//   - url — an already-resolved http(s) source (a debrid or usenet stream URL);
//     the relay fetches it itself.
//   - streamPath — the webdavPath a playback resolve returned; the server
//     re-resolves it to a current URL. Preferred over url for debrid, because
//     some providers' resolutions are not URLs at all and the ones that are
//     expire in minutes.
//
// Explicit TMDB fields always win over what the library knows. A url or
// streamPath seed has no library to fall back on, so it must carry
// contentKind, tmdbId and tmdbTitle.
type SeedRequest struct {
	LocalMediaItemID string `json:"localMediaItemId,omitempty"`
	FilePath         string `json:"filePath,omitempty"`
	SourceURL        string `json:"url,omitempty"`
	StreamPath       string `json:"streamPath,omitempty"`

	ContentKind string `json:"contentKind,omitempty"` // "movie" or "episode"
	TMDBID      string `json:"tmdbId,omitempty"`
	TMDBTitle   string `json:"tmdbTitle,omitempty"`
	TMDBYear    int    `json:"tmdbYear,omitempty"`
	TMDBSeason  int    `json:"tmdbSeason,omitempty"`
	TMDBEpisode int    `json:"tmdbEpisode,omitempty"`
	PosterPath  string `json:"tmdbPosterPath,omitempty"`
	Overview    string `json:"tmdbOverview,omitempty"`
	Runtime     int    `json:"tmdbRuntime,omitempty"`
	Genres      string `json:"tmdbGenres,omitempty"`
}

// SeedResponse mirrors the relay's 202 plus the URL to poll.
type SeedResponse struct {
	JobID      string `json:"jobId"`
	Status     string `json:"status"`
	EntityHint string `json:"entityHint"`
	StatusPath string `json:"statusPath"`
}

// statusProbeTimeout bounds the relay round trip a status poll makes. The
// catalog is cached, so a poll right after a search costs nothing.
const statusProbeTimeout = 5 * time.Second

// Status reports whether p2p is wired up and, when it is, what the relay will
// actually do right now — so a frontend can hide the seed control, and an
// operator can see that the relay is up but has not been allowed to serve
// media, together with the command that fixes it.
//
// It also reports which fields the environment is supplying, because the admin
// settings page renders stored values: without this it would show an empty relay
// URL field beside a working relay and give no hint why.
func (h *PearTubeHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.configMu.RLock()
	relay, autoSeed, resolved := h.relay, h.autoSeed, h.resolved
	h.configMu.RUnlock()

	// Where each effective value came from. Reported whether or not a relay
	// exists: "PEARTUBE_ENABLED is holding this off" is exactly the thing an
	// operator staring at a disabled section needs to be told.
	fromEnv := map[string]bool{
		"relayUrl": resolved.RelayURLFromEnv,
		"enabled":  resolved.EnabledFromEnv,
		"autoSeed": resolved.AutoSeedFromEnv,
	}

	if relay == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":  false,
			"state":    "disabled",
			"autoSeed": autoSeed,
			"fromEnv":  fromEnv,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), statusProbeTimeout)
	defer cancel()
	relayState := relay.Probe(ctx)

	body := map[string]any{
		"enabled":          true,
		"relayUrl":         relayState.RelayURL,
		"reachable":        relayState.Reachable,
		"notOpen":          relayState.NotOpen,
		"seedingAvailable": relayState.SeedingAvailable,
		"catalogEntities":  relayState.CatalogEntities,
		// Whether starting a playback publishes it, so an admin screen can say
		// so rather than leaving the operator to infer it from the environment.
		"autoSeed": autoSeed,
		"state":    p2pStateLabel(relayState),
		"fromEnv":  fromEnv,
	}
	if relayState.Remedy != "" {
		body["remedy"] = relayState.Remedy
	}
	if relayState.Detail != "" {
		body["detail"] = relayState.Detail
	}
	writeJSON(w, http.StatusOK, body)
}

// p2pStateLabel names the relay's condition in one word an admin UI can switch
// on without re-deriving it from the flags.
func p2pStateLabel(state peartube.RelayState) string {
	switch {
	case state.NotOpen:
		return "not_open"
	case !state.Reachable:
		return "unreachable"
	default:
		return "ready"
	}
}

// Seed publishes something this viewer can play into the PearTube swarm.
func (h *PearTubeHandler) Seed(w http.ResponseWriter, r *http.Request) {
	relay := h.currentRelay()
	if relay == nil {
		writeJSONError(w, "peartube relay is not configured", http.StatusServiceUnavailable)
		return
	}
	var req SeedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	submit, err := h.planSeed(r.Context(), relay, req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, localmedia.ErrItemNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, err.Error(), status)
		return
	}

	job, err := submit(r.Context())
	if err != nil {
		// A source the relay will not fetch is the caller's to fix by sending a
		// different one, and says nothing about the relay's health. Anything
		// else — refused for another reason, unreachable, broken — is not.
		var apiErr *peartube.APIError
		if errors.As(err, &apiErr) && peartube.IsSourceRefused(err) {
			message := apiErr.Message
			if message == "" {
				message = apiErr.Error()
			}
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": message,
				"code":  apiErr.Code,
			})
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusAccepted, SeedResponse{
		JobID:      job.JobID,
		Status:     job.Status,
		EntityHint: job.EntityHint,
		StatusPath: "p2p/seed/" + job.JobID,
	})
}

const (
	// autoSeedGuardWindow is how long one title stays claimed by an automatic
	// seed, so a burst of heartbeats, a seek, a stream retry, or two viewers
	// starting the same title cannot enqueue the same whole-file fetch twice.
	//
	// It has to outlast the relay's fetch, because until that finishes the title
	// is not in the catalog and the catalog check cannot see it. Six hours covers
	// a feature-length fetch on a slow link with room to spare, and the claim
	// lapses afterwards, so a seed that failed for a transient reason is retried
	// by a later watch rather than never again.
	autoSeedGuardWindow = 6 * time.Hour

	// autoSeedTimeout bounds one background attempt: a catalog read, a debrid
	// re-resolve, and the relay's acceptance. The relay's own fetch of the file
	// happens after that and is not waited on.
	autoSeedTimeout = 2 * time.Minute
)

// A player's item or series id is namespaced when it came from TMDB
// (`tmdb:movie:603`, `tmdb:tv:1399`, `tmdb:tv:1399:s01e02`). A tvdb- or
// imdb-keyed id carries no TMDB number, and the swarm keys everything by TMDB.
var tmdbPlaybackID = regexp.MustCompile(`(?i)\btmdb:(?:movie|tv|show):([1-9][0-9]{0,9})\b`)

// OnPlaybackStarted publishes what a viewer just started watching into the
// swarm. It is a no-op unless a relay is configured and automatic seeding is on.
//
// The trigger is a playback-progress heartbeat rather than a playback resolve.
// A resolve is not a watch: the prequeue resolves candidates before anyone
// presses play and re-resolves down the candidate list on failure, so hooking it
// would seed titles nobody watched. A resolve also has no TMDB coordinates to
// publish under — it carries an indexer result, not an identified title —
// whereas a heartbeat carries the coordinates and the active source path. And it
// is emphatically not the byte-range path: a single playback opens hundreds of
// range requests, while heartbeats arrive every few seconds and are collapsed to
// one submission by the claim below.
//
// Seeding happens on start, not on a watched threshold. The relay fetches the
// source itself, independently of the viewer, so there is no partial file to
// wait for and waiting buys nothing but a later upload. The cost is that a title
// abandoned after ten seconds is seeded anyway. An operator who wants seeding to
// mean "someone actually watched this" should set PEARTUBE_AUTOSEED=0 and call
// POST /api/p2p/seed from the frontend on whatever signal they prefer.
func (h *PearTubeHandler) OnPlaybackStarted(update models.PlaybackProgressUpdate) {
	plan, ok := h.planAutoSeed(update)
	if !ok {
		return
	}
	// Fire and forget. A heartbeat is on the player's critical path and its
	// request context dies with the response, so the submission gets its own
	// goroutine and its own deadline. Nothing about a failed seed reaches the
	// viewer.
	go plan.submit()
}

// autoSeedPlan is an automatic seed that has claimed its title and is waiting to
// be submitted off the request path.
type autoSeedPlan struct {
	handler *PearTubeHandler
	// relay is the client the plan was made against, carried rather than
	// re-read: a settings save could repoint the relay between the claim and the
	// submission, and this seed belongs to the relay whose catalog it checked.
	relay   *peartube.Client
	request SeedRequest
	key     string
}

// planAutoSeed decides, without touching the network, whether this heartbeat
// should become a seed — and claims the title if so, so that the decision is
// made once even when heartbeats overlap.
func (h *PearTubeHandler) planAutoSeed(update models.PlaybackProgressUpdate) (autoSeedPlan, bool) {
	if h == nil {
		return autoSeedPlan{}, false
	}
	h.configMu.RLock()
	relay, autoSeed := h.relay, h.autoSeed
	h.configMu.RUnlock()
	if relay == nil || !autoSeed {
		return autoSeedPlan{}, false
	}
	request, ok := autoSeedRequest(update)
	if !ok {
		return autoSeedPlan{}, false
	}
	key := peartube.EntityKey(seedCoordinates(request))
	if key == "" {
		return autoSeedPlan{}, false
	}
	if !h.claimAutoSeed(key) {
		return autoSeedPlan{}, false
	}
	return autoSeedPlan{handler: h, relay: relay, request: request, key: key}, true
}

// autoSeedRequest turns a playback heartbeat into the seed request the manual
// endpoint would have received, or reports that this playback is not seedable.
//
// It names the stream path and never a URL. The seed path re-resolves that path
// server-side, which is the only thing that works: a Torbox resolution is an
// internal torrent_id:file_id reference rather than an address, and the debrid
// URLs that are addresses expire in about ten minutes.
func autoSeedRequest(update models.PlaybackProgressUpdate) (SeedRequest, bool) {
	request := SeedRequest{StreamPath: strings.TrimSpace(update.SourcePath)}
	if request.StreamPath == "" {
		// Nothing to re-resolve, and the player's own URL must never be
		// forwarded in its place.
		return SeedRequest{}, false
	}

	switch mediaidentity.NormalizeMediaType(update.MediaType) {
	case "movie":
		request.ContentKind = "movie"
		request.TMDBID = tmdbIDFromPlayback(update.ItemID, update.ExternalIDs)
		request.TMDBTitle = strings.TrimSpace(update.MovieName)
		request.TMDBYear = update.Year
	case "episode":
		// The swarm keys an episode by its series' TMDB id, which is what
		// seriesId carries; an episode's own id is no use here.
		seriesID := strings.TrimSpace(update.SeriesID)
		if seriesID == "" {
			seriesID = update.ItemID
		}
		request.ContentKind = "episode"
		request.TMDBID = tmdbIDFromPlayback(seriesID, update.ExternalIDs)
		request.TMDBTitle = strings.TrimSpace(update.SeriesName)
		request.TMDBSeason = update.SeasonNumber
		request.TMDBEpisode = update.EpisodeNumber
	default:
		// Live TV and anything else has no TMDB coordinates to publish under.
		return SeedRequest{}, false
	}

	// A player that reported no TMDB id or no title gives the relay nothing to
	// publish under. Silence beats a rejected submission on every heartbeat.
	if err := seedCoordinates(request).Validate(); err != nil {
		return SeedRequest{}, false
	}
	return request, true
}

// tmdbIDFromPlayback recovers the numeric TMDB id a seed must be published
// under. The external id map wins: for an episode its `tmdb` entry is the series
// id, which is exactly the coordinate needed, and it is present even when the
// player's own ids came from another provider.
func tmdbIDFromPlayback(id string, externalIDs map[string]string) string {
	if value := strings.TrimSpace(mediaidentity.NormalizeExternalIDs(externalIDs)["tmdb"]); isTMDBID(value) {
		return value
	}
	if match := tmdbPlaybackID.FindStringSubmatch(id); match != nil {
		return match[1]
	}
	return ""
}

// claimAutoSeed reserves a title for one automatic seed, reporting whether the
// caller got it. This is the in-process half of the dedupe; the relay catalog is
// the other half, and only this half can stop two submissions racing each other
// before either reaches the relay.
func (h *PearTubeHandler) claimAutoSeed(key string) bool {
	now := time.Now()
	h.autoSeedMu.Lock()
	defer h.autoSeedMu.Unlock()
	if h.autoSeedClaims == nil {
		h.autoSeedClaims = make(map[string]time.Time)
	}
	if until, held := h.autoSeedClaims[key]; held && until.After(now) {
		return false
	}
	// Lapsed claims are dropped here rather than by a timer: the map only grows
	// when something is played, so the sweep runs exactly as often as it needs to.
	for claimed, until := range h.autoSeedClaims {
		if !until.After(now) {
			delete(h.autoSeedClaims, claimed)
		}
	}
	h.autoSeedClaims[key] = now.Add(autoSeedGuardWindow)
	return true
}

func (h *PearTubeHandler) releaseAutoSeed(key string) {
	h.autoSeedMu.Lock()
	defer h.autoSeedMu.Unlock()
	delete(h.autoSeedClaims, key)
}

// submit performs the claimed seed. It runs on its own goroutine, so every
// outcome ends in a log line and nothing else.
func (p autoSeedPlan) submit() {
	ctx, cancel := context.WithTimeout(context.Background(), autoSeedTimeout)
	defer cancel()

	published, err := p.relay.CatalogHasEntity(ctx, seedCoordinates(p.request))
	switch {
	case err != nil:
		// The relay could not say whether the swarm already has this. Seeding
		// anyway would ask it to fetch a whole file it may already hold, so the
		// attempt is abandoned — and the claim is dropped, because nothing was
		// submitted and a later watch should ask again. That is not a poll: the
		// client caches a failed catalog read for 30s, so the heartbeats of this
		// playback re-read nothing.
		p.handler.releaseAutoSeed(p.key)
		log.Printf("[peartube] autoseed %s: skipped, catalog unavailable: %v", p.key, err)
		return
	case published:
		log.Printf("[peartube] autoseed %s: already served by the swarm", p.key)
		return
	}

	// From here the claim is kept whatever happens. A source the relay refuses
	// will be refused again on the next heartbeat, and one log line per title
	// per guard window is the right amount of noise for that.
	submit, err := p.handler.planSeed(ctx, p.relay, p.request)
	if err != nil {
		log.Printf("[peartube] autoseed %s: not seedable: %v", p.key, err)
		return
	}
	job, err := submit(ctx)
	if err != nil {
		log.Printf("[peartube] autoseed %s: relay refused the seed: %v", p.key, err)
		return
	}
	log.Printf("[peartube] autoseed %s: relay accepted job %s (%s)", p.key, job.JobID, job.Status)
}

// seedCoordinates are the TMDB coordinates a seed request publishes under.
// Shared with the automatic trigger, which needs them before it commits to a
// seed in order to ask the relay whether the swarm already has this title.
func seedCoordinates(req SeedRequest) peartube.ArchiveCoordinates {
	return peartube.ArchiveCoordinates{
		ContentKind: strings.TrimSpace(req.ContentKind),
		TMDBID:      strings.TrimSpace(req.TMDBID),
		TMDBTitle:   strings.TrimSpace(req.TMDBTitle),
		TMDBYear:    req.TMDBYear,
		TMDBSeason:  req.TMDBSeason,
		TMDBEpisode: req.TMDBEpisode,
		PosterPath:  strings.TrimSpace(req.PosterPath),
		Overview:    strings.TrimSpace(req.Overview),
		Runtime:     req.Runtime,
		Genres:      strings.TrimSpace(req.Genres),
	}
}

// planSeed picks the relay transport a seed request needs and validates
// everything that can be checked without a round trip. Only the returned
// closure talks to the relay, so a caller mistake stays a client error.
func (h *PearTubeHandler) planSeed(ctx context.Context, relay *peartube.Client, req SeedRequest) (func(context.Context) (*peartube.ArchiveJob, error), error) {
	coordinates := seedCoordinates(req)

	onDisk := strings.TrimSpace(req.LocalMediaItemID) != "" || strings.TrimSpace(req.FilePath) != ""
	remote := strings.TrimSpace(req.SourceURL) != "" || strings.TrimSpace(req.StreamPath) != ""

	switch {
	case onDisk && remote:
		return nil, errors.New("seed either a local library item or a remote source, not both")

	case remote:
		source, err := h.resolveSeedURL(ctx, req)
		if err != nil {
			return nil, err
		}
		archive := peartube.ArchiveURLRequest{SourceURL: source, ArchiveCoordinates: coordinates}
		if err := archive.Validate(); err != nil {
			return nil, err
		}
		log.Printf("[peartube] seeding %s tmdb=%s title=%q from %s",
			archive.ContentKind, archive.TMDBID, archive.TMDBTitle, seedSourceForLog(source))
		return func(ctx context.Context) (*peartube.ArchiveJob, error) {
			return relay.ArchiveURL(ctx, archive)
		}, nil

	case onDisk:
		archive, err := h.buildArchiveRequest(ctx, req, coordinates)
		if err != nil {
			return nil, err
		}
		log.Printf("[peartube] seeding %s tmdb=%s title=%q path=%q",
			archive.ContentKind, archive.TMDBID, archive.TMDBTitle, archive.FilePath)
		return func(ctx context.Context) (*peartube.ArchiveJob, error) {
			return relay.Archive(ctx, archive)
		}, nil

	default:
		return nil, errors.New("localMediaItemId, filePath, url or streamPath is required")
	}
}

// resolveSeedURL produces the address the relay will fetch.
//
// A streamPath is re-resolved rather than trusted as handed out, and that is
// what makes the debrid case work at all. The URL a debrid resolve returns is
// not always a URL — Torbox's resolution is an internal torrent_id:file_id
// reference that only becomes an address at stream time — and the providers that
// do return a real CDN URL mint short-lived, account-scoped ones, which this
// backend itself re-unrestricts every 10 minutes. Resolving at seed time hands
// the relay the freshest address available.
func (h *PearTubeHandler) resolveSeedURL(ctx context.Context, req SeedRequest) (string, error) {
	streamPath := strings.TrimSpace(req.StreamPath)
	if streamPath == "" {
		return strings.TrimSpace(req.SourceURL), nil
	}
	if h.streams == nil {
		return "", errors.New("stream path resolution is unavailable; seed with an explicit url instead")
	}
	resolved, err := h.streams.GetDirectURL(ctx, streamPath)
	if err != nil {
		return "", fmt.Errorf("resolve streamPath: %w", err)
	}
	if !strings.HasPrefix(resolved, "http://") && !strings.HasPrefix(resolved, "https://") {
		// Local media and recordings resolve to filesystem paths, which the
		// relay cannot fetch. Those seed by localMediaItemId, which uploads.
		return "", errors.New("streamPath resolved to a local file rather than a URL; seed it with localMediaItemId")
	}
	return resolved, nil
}

// seedSourceForLog keeps a debrid CDN token out of the log. Those URLs carry
// account-scoped credentials in the path or the query string.
func seedSourceForLog(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "an unparseable url"
	}
	return parsed.Scheme + "://" + parsed.Host + "/..."
}

// SeedStatus proxies the relay's job status so the frontend polls one origin.
func (h *PearTubeHandler) SeedStatus(w http.ResponseWriter, r *http.Request) {
	relay := h.currentRelay()
	if relay == nil {
		writeJSONError(w, "peartube relay is not configured", http.StatusServiceUnavailable)
		return
	}
	status, err := relay.ArchiveStatus(r.Context(), mux.Vars(r)["jobId"])
	if err != nil {
		var apiErr *peartube.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			writeJSONError(w, "seed job not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// buildArchiveRequest resolves the local-library form of a seed request to a
// file this process may publish.
func (h *PearTubeHandler) buildArchiveRequest(ctx context.Context, req SeedRequest, coordinates peartube.ArchiveCoordinates) (peartube.ArchiveRequest, error) {
	archive := peartube.ArchiveRequest{ArchiveCoordinates: coordinates}

	itemID := strings.TrimSpace(req.LocalMediaItemID)
	if itemID != "" {
		if h.localMedia == nil {
			return archive, errors.New("local media service unavailable")
		}
		item, err := h.localMedia.GetItem(ctx, itemID)
		if err != nil {
			return archive, err
		}
		archive.FilePath = item.FilePath
		applyLocalMediaMetadata(&archive.ArchiveCoordinates, item)
	}

	if path := strings.TrimSpace(req.FilePath); path != "" {
		resolved, err := h.resolveLibraryPath(ctx, path)
		if err != nil {
			return archive, err
		}
		archive.FilePath = resolved
	}

	if archive.FilePath == "" {
		return archive, errors.New("localMediaItemId or filePath is required")
	}
	if err := archive.Validate(); err != nil {
		return archive, err
	}
	return archive, nil
}

// applyLocalMediaMetadata fills in the TMDB coordinates the caller did not
// supply from what the library scanner matched. It never overwrites an explicit
// value: the caller is the one looking at the detail screen.
func applyLocalMediaMetadata(archive *peartube.ArchiveCoordinates, item *models.LocalMediaItem) {
	if archive.TMDBID == "" && item.ExternalIDs != nil {
		archive.TMDBID = strings.TrimSpace(item.ExternalIDs.TMDB)
	}
	if archive.TMDBID == "" {
		// A matched item's title id is its TMDB id; anything else is not a
		// coordinate the relay accepts.
		if id := strings.TrimSpace(item.MatchedTitleID); isTMDBID(id) {
			archive.TMDBID = id
		}
	}
	if archive.TMDBTitle == "" {
		archive.TMDBTitle = strings.TrimSpace(item.MatchedName)
	}
	if archive.TMDBTitle == "" {
		archive.TMDBTitle = strings.TrimSpace(item.DetectedTitle)
	}
	if archive.TMDBYear == 0 {
		archive.TMDBYear = item.MatchedYear
	}
	if archive.TMDBYear == 0 {
		archive.TMDBYear = item.DetectedYear
	}
	if archive.ContentKind == "" {
		if item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
			archive.ContentKind = "episode"
		} else if item.LibraryType == models.LocalMediaLibraryTypeMovie {
			archive.ContentKind = "movie"
		}
	}
	if archive.ContentKind == "episode" {
		if archive.TMDBSeason == 0 {
			archive.TMDBSeason = item.SeasonNumber
		}
		if archive.TMDBEpisode == 0 {
			archive.TMDBEpisode = item.EpisodeNumber
		}
	}
	if archive.Overview == "" {
		archive.Overview = strings.TrimSpace(item.EpisodeOverview)
	}
}

// resolveLibraryPath confirms an explicit path names a real file inside a
// configured library root. Without this, an authenticated account could publish
// any file this process can read into a public swarm.
func (h *PearTubeHandler) resolveLibraryPath(ctx context.Context, path string) (string, error) {
	if h.localMedia == nil {
		return "", errors.New("local media service unavailable")
	}
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if info.IsDir() {
		return "", errors.New("filePath is a directory")
	}
	libraries, err := h.localMedia.ListLibraries(ctx)
	if err != nil {
		return "", err
	}
	for _, library := range libraries {
		root, err := filepath.Abs(filepath.Clean(library.RootPath))
		if err != nil || root == "" {
			continue
		}
		if rel, err := filepath.Rel(root, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", errors.New("filePath is not inside a configured local media library")
}

func isTMDBID(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
