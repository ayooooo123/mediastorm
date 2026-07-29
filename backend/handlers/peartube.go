package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// PearTubeHandler exposes the seeding half of the p2p integration: it takes an
// item this server already holds on disk and publishes it into the PearTube
// swarm, so the next viewer can stream it from here instead of from a provider.
//
// There is no automatic hook onto a watched/completed signal. The only playback
// path where the server holds real bytes on disk is the local media library;
// usenet and debrid playback both stream from somewhere else, and the backend
// never has a file to hand the relay. So this is an explicit endpoint the
// frontend calls when a user opts in to seeding what they watched.
type PearTubeHandler struct {
	relay      *peartube.Client
	localMedia localMediaLibrary
}

func NewPearTubeHandler(relay *peartube.Client, localMedia *localmedia.Service) *PearTubeHandler {
	handler := &PearTubeHandler{relay: relay}
	// A typed nil in an interface is not nil; only assign a real service.
	if localMedia != nil {
		handler.localMedia = localMedia
	}
	return handler
}

// SeedRequest names the bytes to publish and the TMDB coordinates to publish
// them under.
//
// Either localMediaItemId (the server resolves the path and the metadata) or
// filePath (which must live inside a configured local media library root) is
// required. Explicit TMDB fields always win over what the library knows.
type SeedRequest struct {
	LocalMediaItemID string `json:"localMediaItemId,omitempty"`
	FilePath         string `json:"filePath,omitempty"`

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

// Status reports whether p2p is wired up, so a frontend can hide the seed
// control instead of offering an action that always fails.
func (h *PearTubeHandler) Status(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{"enabled": h.relay != nil}
	if h.relay != nil {
		body["relayUrl"] = h.relay.BaseURL()
	}
	writeJSON(w, http.StatusOK, body)
}

// Seed publishes a local file into the PearTube swarm.
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

	archive, err := h.buildArchiveRequest(r.Context(), req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, localmedia.ErrItemNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, err.Error(), status)
		return
	}

	log.Printf("[peartube] seeding %s tmdb=%s title=%q path=%q",
		archive.ContentKind, archive.TMDBID, archive.TMDBTitle, archive.FilePath)

	job, err := h.relay.Archive(r.Context(), archive)
	if err != nil {
		var apiErr *peartube.APIError
		if errors.As(err, &apiErr) {
			writeJSONError(w, apiErr.Error(), http.StatusBadGateway)
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

func (h *PearTubeHandler) buildArchiveRequest(ctx context.Context, req SeedRequest) (peartube.ArchiveRequest, error) {
	archive := peartube.ArchiveRequest{
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
		applyLocalMediaMetadata(&archive, item)
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
	if archive.ContentKind == "" {
		return archive, errors.New("contentKind is required (movie or episode)")
	}
	return archive, nil
}

// applyLocalMediaMetadata fills in the TMDB coordinates the caller did not
// supply from what the library scanner matched. It never overwrites an explicit
// value: the caller is the one looking at the detail screen.
func applyLocalMediaMetadata(archive *peartube.ArchiveRequest, item *models.LocalMediaItem) {
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
