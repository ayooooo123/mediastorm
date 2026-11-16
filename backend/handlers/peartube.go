package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"novastream/config"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/peartube"
	"novastream/services/streaming"
)

const (
	// A played title is checked against the relay at most this often.
	pearTubeClaimTTL       = 30 * time.Minute
	pearTubeArchiveTimeout = time.Minute
	pearTubeStatusTimeout  = 5 * time.Second
	// The relay drops tracker entries with longer titles.
	pearTubeMaxTitleBytes = 300
)

// PearTubeHandler archives what users play to the configured PearTube relay
// and reports the relay's status.
type pearTubeClaim struct {
	path     string
	title    string
	at       time.Time
	inflight bool
	dirty    bool
}

type PearTubeHandler struct {
	sources *peartube.Sources

	mu         sync.Mutex
	client     *peartube.Client
	relayURL   string
	archive    bool
	sourceBase string
	configErr  string
	claims     map[string]pearTubeClaim
	imdb       pearTubeIMDBResolver
	lookups    map[string]time.Time
}

// pearTubeIMDBResolver finds the IMDb ID of a playback whose client sent none.
// *metadata.Service implements it.
type pearTubeIMDBResolver interface {
	IMDBIDForTMDB(ctx context.Context, mediaType string, tmdbID int64) string
	ResolveIMDBID(ctx context.Context, title, mediaType string, year int) string
}

var _ playbackArchiver = (*PearTubeHandler)(nil)

// NewPearTubeHandler serves archived playback sources through MediaStorm's providers.
func NewPearTubeHandler(streams streaming.Provider) *PearTubeHandler {
	return &PearTubeHandler{
		sources: peartube.NewSources(streams),
		claims:  make(map[string]pearTubeClaim),
		lookups: make(map[string]time.Time),
	}
}

// SetIMDBResolver lets playbacks whose client sent no IMDb ID be archived.
func (h *PearTubeHandler) SetIMDBResolver(resolver pearTubeIMDBResolver) {
	h.mu.Lock()
	h.imdb = resolver
	h.mu.Unlock()
}

// SetVideoHandler attaches VideoHandler's external HTTP proxy so addon and WebDAV
// URLs keep their headers, credentials, and redirect recovery when archived.
func (h *PearTubeHandler) SetVideoHandler(v *VideoHandler) {
	if v == nil {
		return
	}
	h.sources.SetExternalProxy(func(w http.ResponseWriter, r *http.Request, target string) {
		_, _ = v.proxyExternalURL(w, r, target)
	})
}

// Sources serves peartube.SourceRoute: authenticated Range fetches for the relay.
func (h *PearTubeHandler) Sources() http.Handler { return h.sources }
// ApplyPearTubeSettings installs the relay configuration. It runs at startup
// and on every settings save.
func (h *PearTubeHandler) ApplyPearTubeSettings(settings config.Settings) {
	cfg := settings.PearTubeConfig()
	var client *peartube.Client
	configErr := ""
	if cfg.RelayURL != "" {
		var err error
		if client, err = peartube.New(cfg.RelayURL, cfg.Secret); err != nil {
			log.Printf("[peartube] relay disabled: %v", err)
			configErr = err.Error()
		}
	}
	// The relay fetches MediaStorm-only sources here. It is usually a LAN
	// address, unlike the public external backend URL used for invite links.
	sourceBase := cfg.SourceURL
	if sourceBase == "" {
		sourceBase = settings.Server.ExternalBackendURL
	}
	h.mu.Lock()
	h.client = client
	h.relayURL = cfg.RelayURL
	h.archive = cfg.ArchiveEnabled
	h.sourceBase = strings.TrimRight(sourceBase, "/")
	h.configErr = configErr
	h.mu.Unlock()
}

type pearTubeStatusResponse struct {
	State          string           `json:"state"` // disabled | misconfigured | unreachable | ready
	RelayURL       string           `json:"relayUrl,omitempty"`
	ArchiveEnabled bool             `json:"archiveEnabled"`
	Relay          *peartube.Status `json:"relay,omitempty"`
	Error          string           `json:"error,omitempty"`
}

// Status reports the configured relay and its /v1/status.
func (h *PearTubeHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	client := h.client
	response := pearTubeStatusResponse{RelayURL: h.relayURL, ArchiveEnabled: h.archive, Error: h.configErr}
	h.mu.Unlock()

	switch {
	case response.RelayURL == "":
		response.State = "disabled"
	case client == nil:
		response.State = "misconfigured"
	default:
		ctx, cancel := context.WithTimeout(r.Context(), pearTubeStatusTimeout)
		defer cancel()
		if status, err := client.Status(ctx); err != nil {
			response.State = "unreachable"
			response.Error = err.Error()
		} else {
			response.State = "ready"
			response.Relay = &status
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// OnPlaybackStarted archives the played title to the relay when archiving is
// on. It is called for every playback signal, so it only claims the title here
// and does the relay work off the caller's path.
func (h *PearTubeHandler) OnPlaybackStarted(update models.PlaybackProgressUpdate) {
	h.mu.Lock()
	client, archive, sourceBase, resolver := h.client, h.archive, h.sourceBase, h.imdb
	h.mu.Unlock()
	if client == nil || !archive {
		return
	}
	path := strings.TrimSpace(update.SourcePath)
	if path == "" || peartube.IsStreamURL(path) {
		return
	}
	title := pearTubeTitle(update)
	if id := pearTubePlaybackID(update, mediaidentity.NormalizeExternalIDs(update.ExternalIDs)["imdb"]); id != "" {
		h.archivePlayback(client, sourceBase, id, path, title)
		return
	}
	// Clients send no IMDb ID when their title details lack one. Look it up
	// once per source and off the caller's path: progress heartbeats call here.
	if !h.claimLookup(path) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pearTubeArchiveTimeout)
		imdbID, via := lookupPlaybackIMDBID(ctx, resolver, update)
		cancel()
		id := pearTubePlaybackID(update, imdbID)
		if id == "" {
			log.Printf("[peartube] playback not archivable: no IMDb ID for %q (mediaType=%q itemId=%q seriesId=%q)",
				title, update.MediaType, update.ItemID, update.SeriesID)
			return
		}
		log.Printf("[peartube] client sent no IMDb ID for %q; resolved %s from %s", title, imdbID, via)
		h.archivePlayback(client, sourceBase, id, path, title)
	}()
}

// archivePlayback claims id and asks the relay to archive it in the background.
func (h *PearTubeHandler) archivePlayback(client *peartube.Client, sourceBase, id, path, title string) {
	if !h.claim(id, path, title) {
		return
	}
	go func(path, title string) {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), pearTubeArchiveTimeout)
			err := h.archiveTitle(ctx, client, sourceBase, id, title, path)
			cancel()
			if err != nil {
				log.Printf("[peartube] archive %s failed: %v", id, err)
			}
			var again bool
			if path, title, again = h.finishClaim(id, path, err != nil); !again {
				return
			}
		}
	}(path, title)
}

// claimLookup reports whether path still needs an IMDb lookup. Each source is
// looked up at most once per claim TTL, found or not.
func (h *PearTubeHandler) claimLookup(path string) bool {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, at := range h.lookups {
		if now.Sub(at) > pearTubeClaimTTL {
			delete(h.lookups, key)
		}
	}
	if _, ok := h.lookups[path]; ok {
		return false
	}
	h.lookups[path] = now
	return true
}

// lookupPlaybackIMDBID finds a playback's IMDb ID from its own IDs, then by
// title and year as the debrid search does. It also names the source used.
// Episode archives use the show's IMDb ID, so episodes consult show IDs only.
func lookupPlaybackIMDBID(ctx context.Context, resolver pearTubeIMDBResolver, update models.PlaybackProgressUpdate) (string, string) {
	if resolver == nil {
		return "", ""
	}
	var tmdbType, name string
	var ids []string
	switch mediaidentity.NormalizeMediaType(update.MediaType) {
	case "movie":
		tmdbType, name = "movie", update.MovieName
		ids = []string{mediaidentity.NormalizeExternalIDs(update.ExternalIDs)["tmdb"], update.ItemID}
	case "episode":
		tmdbType, name = "tv", update.SeriesName
		ids = []string{update.SeriesID, mediaidentity.InferSeriesIDFromEpisodeItemID(update.ItemID)}
	default:
		return "", ""
	}
	for _, raw := range ids {
		raw = strings.TrimSpace(raw)
		provider, value := mediaidentity.SeriesProviderAndID(raw)
		switch {
		case provider == "imdb":
			return value, raw
		case provider == "" && strings.Contains(raw, ":"), provider != "" && provider != "tmdb":
			continue
		case provider == "":
			value = raw
		}
		if tmdbID, err := strconv.ParseInt(value, 10, 64); err == nil && tmdbID > 0 {
			if imdbID := resolver.IMDBIDForTMDB(ctx, tmdbType, tmdbID); imdbID != "" {
				return imdbID, fmt.Sprintf("tmdb:%s:%d", tmdbType, tmdbID)
			}
		}
	}
	if name = strings.TrimSpace(name); name != "" {
		if imdbID := resolver.ResolveIMDBID(ctx, name, update.MediaType, update.Year); imdbID != "" {
			return imdbID, "title search"
		}
	}
	return "", ""
}

// archiveTitle asks the relay to acquire id unless it already holds it or is
// already fetching it.
func (h *PearTubeHandler) archiveTitle(ctx context.Context, client *peartube.Client, sourceBase, id, title, path string) error {
	results, err := client.Search(ctx, id)
	if err != nil {
		return err
	}
	for _, result := range results {
		if result.Local {
			return nil
		}
	}
	jobs, err := client.Jobs(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.ID != id {
			continue
		}
		if job.Active() {
			return nil
		}
		if job.Status == peartube.JobFailed {
			_, _ = client.Cancel(ctx, job.JobID)
		}
	}
	sourceURL, headers, err := h.archiveSource(sourceBase, path)
	if err != nil {
		return err
	}
	job, err := client.Acquire(ctx, id, title, sourceURL, headers)
	if err != nil {
		return err
	}
	log.Printf("[peartube] archiving %s (%q) as relay job %s", id, title, job.JobID)
	return nil
}

// archiveSource routes every source through MediaStorm's SourceRoute so the relay
// shares MediaStorm's egress IP, debrid unrestrict/403-refresh, RAR slicing, and addon headers.
func (h *PearTubeHandler) archiveSource(sourceBase, path string) (string, map[string]string, error) {
	for _, prefix := range []string{"/webdav/", "webdav/"} {
		if rest, found := strings.CutPrefix(path, prefix); found {
			path = "/" + rest
			break
		}
	}
	if sourceBase == "" {
		return "", nil, errors.New("set the external backend URL or PearTube sourceUrl so the relay can fetch through MediaStorm")
	}
	token, err := h.sources.Issue(path)
	if err != nil {
		return "", nil, err
	}
	return sourceBase + peartube.SourceRoute, map[string]string{"Authorization": "Bearer " + token}, nil
}

// claim serializes archive checks per id so fast playback migrations never enqueue duplicate relay jobs.
func (h *PearTubeHandler) claim(id, path, title string) bool {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, claimed := range h.claims {
		if !claimed.inflight && now.Sub(claimed.at) > pearTubeClaimTTL {
			delete(h.claims, key)
		}
	}
	if held, ok := h.claims[id]; ok {
		if held.path == path {
			return false
		}
		if held.inflight {
			held.path, held.title, held.at, held.dirty = path, title, now, true
			h.claims[id] = held
			return false
		}
	}
	h.claims[id] = pearTubeClaim{path: path, title: title, at: now, inflight: true}
	return true
}

func (h *PearTubeHandler) finishClaim(id, path string, failed bool) (string, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	held, ok := h.claims[id]
	if !ok {
		return "", "", false
	}
	if held.dirty && held.path != path {
		held.dirty = false
		h.claims[id] = held
		return held.path, held.title, true
	}
	if failed {
		delete(h.claims, id)
	} else {
		held.inflight = false
		h.claims[id] = held
	}
	return "", "", false
}

// pearTubePlaybackID is the relay id of a played movie or episode, or "" when
// imdbID is not an IMDb ID.
func pearTubePlaybackID(update models.PlaybackProgressUpdate, imdbID string) string {
	switch mediaidentity.NormalizeMediaType(update.MediaType) {
	case "movie":
		return peartube.MediaID(imdbID, 0, 0)
	case "episode":
		if update.SeasonNumber <= 0 || update.EpisodeNumber <= 0 {
			return ""
		}
		return peartube.MediaID(imdbID, update.SeasonNumber, update.EpisodeNumber)
	}
	return ""
}

// pearTubeTitle names the archive like a release, since search results are
// ranked and filtered by parsing their titles.
func pearTubeTitle(update models.PlaybackProgressUpdate) string {
	title := strings.TrimSpace(update.ReleaseTitle)
	switch {
	case title != "":
	case mediaidentity.NormalizeMediaType(update.MediaType) == "episode":
		title = fmt.Sprintf("%s S%02dE%02d", strings.TrimSpace(update.SeriesName), update.SeasonNumber, update.EpisodeNumber)
	case update.Year > 0:
		title = fmt.Sprintf("%s %d", strings.TrimSpace(update.MovieName), update.Year)
	default:
		title = strings.TrimSpace(update.MovieName)
	}
	if len(title) <= pearTubeMaxTitleBytes {
		return title
	}
	cut := pearTubeMaxTitleBytes
	for cut > 0 && !utf8.RuneStart(title[cut]) {
		cut--
	}
	return title[:cut]
}
