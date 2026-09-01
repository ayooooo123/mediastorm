package scrob

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/realtimesessions"
)

type realtimeSession struct {
	remoteKey    string
	account      config.ScrobAccount
	token        string
	lastSent     time.Time
	lastActivity time.Time
	paused       bool
}

// ScrobbleStateTracker mirrors local playback into Scrob's Now Playing list.
// Completed history is still written by Scrobbler, so stopping a session only
// removes its transient Now Playing row.
type ScrobbleStateTracker struct {
	mu              sync.Mutex
	sessions        map[string]*realtimeSession
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
	if refreshInterval <= 0 {
		refreshInterval = 15 * time.Second
	}
	return &ScrobbleStateTracker{
		sessions:        make(map[string]*realtimeSession),
		client:          client,
		scrobbler:       scrobbler,
		refreshInterval: refreshInterval,
		staleTimeout:    2 * time.Minute,
	}
}

func realtimeSessionKey(userID string, update models.PlaybackProgressUpdate) string {
	return userID + ":" + update.MediaType + ":" + strings.ToLower(update.ItemID)
}

func (t *ScrobbleStateTracker) HandleProgressUpdate(userID string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if !t.scrobbler.IsEnabledForUser(userID) {
		return
	}
	start, ok := buildManualSessionStart(update)
	if !ok {
		return
	}

	key := realtimeSessionKey(userID, update)
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	session := t.sessions[key]
	if session == nil {
		if !t.registry.CanStart("scrob", userID, update) {
			return
		}
		account := t.scrobbler.getAccountForUser(userID)
		if !scrobAccountCanPush(account) {
			return
		}
		accountCopy := *account
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		token, err := t.scrobbler.login(ctx, &accountCopy)
		if err != nil {
			cancel()
			log.Printf("[scrob-now-playing] login failed for %s: %v", key, err)
			return
		}
		response, err := t.client.StartSession(ctx, accountCopy.BaseURL, accountCopy.APIKey, token, start)
		cancel()
		if err != nil {
			log.Printf("[scrob-now-playing] start failed for %s: %v", key, err)
			return
		}
		session = &realtimeSession{remoteKey: response.SessionKey, account: accountCopy, token: token}
		t.sessions[key] = session
		t.registry.Record("scrob", userID, "playing", response.SessionKey, update, percentWatched)
	}

	session.lastActivity = now
	if !session.lastSent.IsZero() && session.paused == update.IsPaused && now.Sub(session.lastSent) < t.refreshInterval {
		return
	}
	state := "playing"
	if update.IsPaused {
		state = "paused"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := t.client.UpdateSession(ctx, session.account.BaseURL, session.account.APIKey, session.token, session.remoteKey, ManualSessionUpdate{
		ProgressSeconds: max(0, int(math.Round(update.Position))),
		State:           state,
	})
	cancel()
	if isUnauthorized(err) {
		ctx, cancel = context.WithTimeout(context.Background(), 45*time.Second)
		if token, loginErr := t.scrobbler.login(ctx, &session.account); loginErr == nil {
			session.token = token
			err = t.client.UpdateSession(ctx, session.account.BaseURL, session.account.APIKey, session.token, session.remoteKey, ManualSessionUpdate{
				ProgressSeconds: max(0, int(math.Round(update.Position))), State: state,
			})
		} else {
			err = loginErr
		}
		cancel()
	}
	if err != nil {
		log.Printf("[scrob-now-playing] update failed for %s: %v", key, err)
		return
	}
	session.lastSent = now
	session.paused = update.IsPaused
	t.registry.Record("scrob", userID, state, session.remoteKey, update, percentWatched)
}

func (t *ScrobbleStateTracker) StopSession(userID string, update models.PlaybackProgressUpdate, _ float64) {
	t.stop(realtimeSessionKey(userID, update))
}

func (t *ScrobbleStateTracker) ClearSession(userID string, update models.PlaybackProgressUpdate) {
	t.stop(realtimeSessionKey(userID, update))
}

func (t *ScrobbleStateTracker) stop(key string) {
	t.mu.Lock()
	session := t.sessions[key]
	delete(t.sessions, key)
	t.mu.Unlock()
	if session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := t.client.StopSession(ctx, session.account.BaseURL, session.account.APIKey, session.token, session.remoteKey)
	cancel()
	if isUnauthorized(err) {
		ctx, cancel = context.WithTimeout(context.Background(), 45*time.Second)
		if token, loginErr := t.scrobbler.login(ctx, &session.account); loginErr == nil {
			err = t.client.StopSession(ctx, session.account.BaseURL, session.account.APIKey, token, session.remoteKey)
		} else {
			err = loginErr
		}
		cancel()
	}
	if err != nil {
		log.Printf("[scrob-now-playing] stop failed for %s: %v", key, err)
		return
	}
	parts := strings.SplitN(key, ":", 3)
	if len(parts) == 3 {
		t.registry.Remove("scrob", parts[0], models.PlaybackProgressUpdate{MediaType: parts[1], ItemID: parts[2]})
	}
}

func (t *ScrobbleStateTracker) CleanupRealtimeSession(ctx context.Context, session models.RealtimeScrobbleSession) error {
	account := t.scrobbler.getAccountForUser(session.UserID)
	if !scrobAccountCanPush(account) {
		return fmt.Errorf("no Scrob credentials for user %s", session.UserID)
	}
	token, err := t.scrobbler.login(ctx, account)
	if err != nil {
		return err
	}
	if strings.TrimSpace(session.RemoteKey) == "" {
		return fmt.Errorf("Scrob session has no remote key")
	}
	return t.client.StopSession(ctx, account.BaseURL, account.APIKey, token, session.RemoteKey)
}

func isUnauthorized(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 401")
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
	cutoff := time.Now().Add(-t.staleTimeout)
	var stale []string
	t.mu.Lock()
	for key, session := range t.sessions {
		if session.lastActivity.Before(cutoff) {
			stale = append(stale, key)
		}
	}
	t.mu.Unlock()
	for _, key := range stale {
		log.Printf("[scrob-now-playing] cleaning up stale session: %s", key)
		t.stop(key)
	}
}

func buildManualSessionStart(update models.PlaybackProgressUpdate) (ManualSessionStart, bool) {
	runtime := 0
	if update.Duration > 0 {
		runtime = int(math.Ceil(update.Duration / 60))
	}
	switch update.MediaType {
	case "movie":
		tmdbID := positiveInt(update.ExternalIDs["tmdb"])
		if tmdbID == 0 {
			tmdbID = idFromPrefixes(update.ItemID, "tmdb:movie:", "tmdb:")
		}
		if tmdbID == 0 {
			return ManualSessionStart{}, false
		}
		title := strings.TrimSpace(update.MovieName)
		if title == "" {
			title = update.ItemID
		}
		return ManualSessionStart{TMDBID: tmdbID, MediaType: "movie", Title: title, Runtime: runtime}, true
	case "episode":
		tmdbID := positiveInt(update.ExternalIDs["episodeTmdb"])
		showTMDBID := positiveInt(update.ExternalIDs["tmdb"])
		if showTMDBID == 0 {
			showTMDBID = idFromPrefixes(update.SeriesID, "tmdb:tv:", "tmdb:")
		}
		// Scrob can only resolve an episode session deterministically from the
		// episode's own TMDB ID. Starting with only the show ID makes Scrob create
		// a new temporary media row on every backend restart; those orphaned
		// sessions can later be auto-completed as watched.
		if tmdbID == 0 {
			return ManualSessionStart{}, false
		}
		season, episode := update.SeasonNumber, update.EpisodeNumber
		title := strings.TrimSpace(update.EpisodeName)
		if title == "" {
			title = update.ItemID
		}
		return ManualSessionStart{
			TMDBID: tmdbID, MediaType: "episode", Title: title, Runtime: runtime,
			ShowTMDBID: showTMDBID, SeasonNumber: &season, EpisodeNumber: &episode,
		}, true
	default:
		return ManualSessionStart{}, false
	}
}

func idFromPrefixes(value string, prefixes ...string) int {
	value = strings.TrimSpace(value)
	for _, prefix := range prefixes {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			n, _ := strconv.Atoi(value[len(prefix):])
			if n > 0 {
				return n
			}
		}
	}
	return 0
}
