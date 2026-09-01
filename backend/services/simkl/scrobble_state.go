package simkl

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
	"novastream/services/realtimesessions"
)

type scrobbleState int

const (
	stateIdle scrobbleState = iota
	stateWatching
	statePaused
)

type scrobbleSession struct {
	state       scrobbleState
	lastAPICall time.Time
	progress    float64
	update      models.PlaybackProgressUpdate
}

// ScrobbleStateTracker maps playback progress updates to Simkl scrobble events.
type ScrobbleStateTracker struct {
	mu              sync.Mutex
	sessions        map[string]*scrobbleSession
	client          *Client
	scrobbler       *Scrobbler
	refreshInterval time.Duration
	staleTimeout    time.Duration
	registry        *realtimesessions.Registry
}

func (t *ScrobbleStateTracker) SetSessionRegistry(registry *realtimesessions.Registry) {
	t.registry = registry
}

func NewScrobbleStateTracker(client *Client, scrobbler *Scrobbler, refreshInterval time.Duration) *ScrobbleStateTracker {
	return &ScrobbleStateTracker{
		sessions:        make(map[string]*scrobbleSession),
		client:          client,
		scrobbler:       scrobbler,
		refreshInterval: refreshInterval,
		staleTimeout:    2 * refreshInterval,
	}
}

func sessionKey(userID, mediaType, itemID string) string {
	return userID + ":" + mediaType + ":" + strings.ToLower(itemID)
}

func (t *ScrobbleStateTracker) HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := sessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	sess, exists := t.sessions[key]
	if !exists {
		sess = &scrobbleSession{state: stateIdle}
		t.sessions[key] = sess
	}
	sess.progress = percentWatched
	sess.update = update
	t.mu.Unlock()

	account := t.scrobbler.getAccountForUser(userID)
	if account == nil || account.ClientID == "" || account.AccessToken == "" {
		return
	}

	req := BuildScrobbleRequest(update, percentWatched)

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if update.IsPaused {
		if sess.state == stateWatching {
			if resp, err := t.client.ScrobblePause(account.ClientID, account.AccessToken, req); err != nil {
				log.Printf("[simkl] pause failed for %s: %v", key, err)
			} else {
				logScrobbleSuccess("pause", userID, key, req, resp)
				sess.state = statePaused
				sess.lastAPICall = now
				t.registry.Record("simkl", userID, "paused", "", update, percentWatched)
			}
		}
		return
	}

	switch sess.state {
	case stateIdle, statePaused:
		if !t.registry.CanStart("simkl", userID, update) {
			return
		}
		if resp, err := t.client.ScrobbleStart(account.ClientID, account.AccessToken, req); err != nil {
			log.Printf("[simkl] start failed for %s: %v", key, err)
		} else {
			logScrobbleSuccess("start", userID, key, req, resp)
			sess.state = stateWatching
			sess.lastAPICall = now
			t.registry.Record("simkl", userID, "playing", "", update, percentWatched)
		}
	case stateWatching:
		if now.Sub(sess.lastAPICall) >= t.refreshInterval {
			if resp, err := t.client.ScrobbleStart(account.ClientID, account.AccessToken, req); err != nil {
				log.Printf("[simkl] refresh failed for %s: %v", key, err)
			} else {
				logScrobbleSuccess("refresh", userID, key, req, resp)
				sess.lastAPICall = now
				t.registry.Record("simkl", userID, "playing", "", update, percentWatched)
			}
		}
	}
}

func (t *ScrobbleStateTracker) StopSession(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}

	key := sessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	_, exists := t.sessions[key]
	if exists {
		delete(t.sessions, key)
	}
	t.mu.Unlock()

	account := t.scrobbler.getAccountForUser(userID)
	if account == nil || account.ClientID == "" || account.AccessToken == "" {
		return
	}

	req := BuildScrobbleRequest(update, percentWatched)
	resp, err := t.client.ScrobbleStop(account.ClientID, account.AccessToken, req)
	if err != nil {
		log.Printf("[simkl] stop failed for %s: %v", key, err)
		return
	}
	logScrobbleSuccess("stop", userID, key, req, resp)
	t.scrobbler.noteRecentStop(userID, update)
	t.registry.Remove("simkl", userID, update)
}

func (t *ScrobbleStateTracker) CleanupRealtimeSession(_ context.Context, session models.RealtimeScrobbleSession) error {
	account := t.scrobbler.getAccountForUser(session.UserID)
	if account == nil || account.ClientID == "" || account.AccessToken == "" {
		return fmt.Errorf("no Simkl credentials for user %s", session.UserID)
	}
	req := BuildScrobbleRequest(session.Update, session.PercentWatched)
	_, err := t.client.ScrobbleStop(account.ClientID, account.AccessToken, req)
	return err
}

func logScrobbleSuccess(event, userID, key string, req ScrobbleRequest, resp *ScrobbleResponse) {
	responseAction := ""
	responseProgress := float64(0)
	responseID := int64(0)
	if resp != nil {
		responseAction = resp.Action
		responseProgress = resp.Progress
		responseID = resp.ID
	}

	log.Printf("[simkl-audit] event=%s user=%s key=%s requestProgress=%.2f responseAction=%q responseProgress=%.2f responseID=%d",
		event, userID, key, req.Progress, responseAction, responseProgress, responseID)
}

// ClearSession removes a local realtime scrobble session without sending
// scrobble/stop. Use this when another path is already writing watched history.
func (t *ScrobbleStateTracker) ClearSession(userID string, update models.PlaybackProgressUpdate) {
	key := sessionKey(userID, update.MediaType, update.ItemID)

	t.mu.Lock()
	delete(t.sessions, key)
	t.mu.Unlock()
}

func (t *ScrobbleStateTracker) StartCleanup(ctx context.Context) {
	ticker := time.NewTicker(t.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.cleanupStaleSessions()
		}
	}
}

func (t *ScrobbleStateTracker) cleanupStaleSessions() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for key, sess := range t.sessions {
		if now.Sub(sess.lastAPICall) > t.staleTimeout {
			log.Printf("[simkl] cleaning up stale session: %s", key)
			delete(t.sessions, key)
		}
	}
}

func BuildScrobbleRequest(update models.PlaybackProgressUpdate, percentWatched float64) ScrobbleRequest {
	req := ScrobbleRequest{
		Progress: math.Round(percentWatched*100) / 100,
	}
	ids := externalIDsToIDs(update.ExternalIDs)
	if update.MediaType == "movie" {
		req.Movie = &Movie{
			Title: update.MovieName,
			Year:  update.Year,
			IDs:   ids,
		}
	} else if update.MediaType == "episode" {
		req.Show = &Show{
			Title: update.SeriesName,
			IDs:   seriesIDToIDs(update.SeriesID, update.ExternalIDs),
		}
		req.Episode = &Episode{
			Season: update.SeasonNumber,
			Number: update.EpisodeNumber,
		}
	}
	return req
}

func externalIDsToIDs(extIDs map[string]string) IDs {
	ids := IDs{}
	if v, ok := extIDs["tmdb"]; ok {
		ids.TMDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["tvdb"]; ok {
		ids.TVDB, _ = strconv.Atoi(v)
	}
	if v, ok := extIDs["imdb"]; ok {
		ids.IMDB = v
	}
	if v, ok := extIDs["simkl"]; ok {
		ids.Simkl, _ = strconv.Atoi(v)
	}
	return ids
}

func seriesIDToIDs(seriesID string, extIDs map[string]string) IDs {
	ids := externalIDsToIDs(extIDs)
	if ids.TVDB == 0 && ids.TMDB == 0 && ids.IMDB == "" && seriesID != "" {
		parts := strings.Split(seriesID, ":")
		if len(parts) >= 2 {
			provider := strings.ToLower(parts[0])
			numericID := parts[len(parts)-1]
			switch provider {
			case "tvdb":
				ids.TVDB, _ = strconv.Atoi(numericID)
			case "tmdb":
				ids.TMDB, _ = strconv.Atoi(numericID)
			case "imdb":
				ids.IMDB = "tt" + strings.TrimPrefix(numericID, "tt")
			}
		}
	}
	return ids
}
