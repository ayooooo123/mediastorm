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
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

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
// There is no automatic hook onto a watched/completed signal: seeding stays an
// explicit call the frontend makes when a user opts in. An automatic
// watch-threshold trigger would attach in HistoryHandler.UpdatePlaybackProgress
// (handlers/history.go), which already receives the watched fraction and the
// item's coordinates on every progress write; it would call seed with the same
// fields the frontend sends here.
type PearTubeHandler struct {
	relay      *peartube.Client
	localMedia localMediaLibrary
	streams    streamURLResolver
}

func NewPearTubeHandler(relay *peartube.Client, localMedia *localmedia.Service) *PearTubeHandler {
	handler := &PearTubeHandler{relay: relay}
	// A typed nil in an interface is not nil; only assign a real service.
	if localMedia != nil {
		handler.localMedia = localMedia
	}
	return handler
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
func (h *PearTubeHandler) Status(w http.ResponseWriter, r *http.Request) {
	if h.relay == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "state": "disabled"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), statusProbeTimeout)
	defer cancel()
	relayState := h.relay.Probe(ctx)

	body := map[string]any{
		"enabled":          true,
		"relayUrl":         relayState.RelayURL,
		"reachable":        relayState.Reachable,
		"notOpen":          relayState.NotOpen,
		"seedingAvailable": relayState.SeedingAvailable,
		"catalogEntities":  relayState.CatalogEntities,
		"state":            p2pStateLabel(relayState),
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
	if h.relay == nil {
		writeJSONError(w, "peartube relay is not configured", http.StatusServiceUnavailable)
		return
	}
	var req SeedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	submit, err := h.planSeed(r.Context(), req)
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

// planSeed picks the relay transport a seed request needs and validates
// everything that can be checked without a round trip. Only the returned
// closure talks to the relay, so a caller mistake stays a client error.
func (h *PearTubeHandler) planSeed(ctx context.Context, req SeedRequest) (func(context.Context) (*peartube.ArchiveJob, error), error) {
	coordinates := peartube.ArchiveCoordinates{
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
			return h.relay.ArchiveURL(ctx, archive)
		}, nil

	case onDisk:
		archive, err := h.buildArchiveRequest(ctx, req, coordinates)
		if err != nil {
			return nil, err
		}
		log.Printf("[peartube] seeding %s tmdb=%s title=%q path=%q",
			archive.ContentKind, archive.TMDBID, archive.TMDBTitle, archive.FilePath)
		return func(ctx context.Context) (*peartube.ArchiveJob, error) {
			return h.relay.Archive(ctx, archive)
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
	if h.relay == nil {
		writeJSONError(w, "peartube relay is not configured", http.StatusServiceUnavailable)
		return
	}
	status, err := h.relay.ArchiveStatus(r.Context(), mux.Vars(r)["jobId"])
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
