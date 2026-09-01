package realtimesessions

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

const DefaultCleanupInterval = time.Minute

type Store interface {
	Upsert(ctx context.Context, session *models.RealtimeScrobbleSession) error
	List(ctx context.Context) ([]models.RealtimeScrobbleSession, error)
	Delete(ctx context.Context, provider, userID, mediaType, itemID string) error
}

type Cleaner interface {
	CleanupRealtimeSession(ctx context.Context, session models.RealtimeScrobbleSession) error
}

type ActivePlaybackProvider interface {
	IsPlaybackActive(userID string, update models.PlaybackProgressUpdate) bool
}

// Registry records successful provider-side starts and owns the single cleanup
// worker shared by all realtime scrobblers.
type Registry struct {
	store           Store
	mu              sync.RWMutex
	cleaners        map[string]Cleaner
	active          ActivePlaybackProvider
	blocked         map[string]bool
	recoveryPending bool
	interval        time.Duration
}

func New(store Store, interval time.Duration) *Registry {
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	return &Registry{
		store: store, cleaners: make(map[string]Cleaner), blocked: make(map[string]bool), interval: interval,
	}
}

func (r *Registry) RegisterCleaner(provider string, cleaner Cleaner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleaners[strings.ToLower(strings.TrimSpace(provider))] = cleaner
}

func (r *Registry) SetActivePlaybackProvider(provider ActivePlaybackProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = provider
}

// CanStart reports whether a tracker may create a new provider-side session.
// A failed restart recovery blocks only the affected item so its persisted
// remote session key cannot be overwritten before cleanup succeeds.
func (r *Registry) CanStart(provider, userID string, update models.PlaybackProgressUpdate) bool {
	if r == nil || r.store == nil {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.recoveryPending && !r.blocked[sessionRecordKey(provider, userID, update.MediaType, update.ItemID)]
}

func (r *Registry) Record(provider, userID, state, remoteKey string, update models.PlaybackProgressUpdate, percentWatched float64) {
	if r == nil || r.store == nil {
		return
	}
	if !r.CanStart(provider, userID, update) {
		log.Printf("[realtime-sessions] refusing to overwrite unrecovered %s session user=%s item=%s", provider, userID, update.ItemID)
		return
	}
	now := time.Now().UTC()
	session := models.RealtimeScrobbleSession{
		Provider: strings.ToLower(strings.TrimSpace(provider)), UserID: userID,
		MediaType: strings.ToLower(update.MediaType), ItemID: strings.ToLower(update.ItemID),
		RemoteKey: remoteKey, State: state, PercentWatched: percentWatched,
		Update: update, StartedAt: now, UpdatedAt: now,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.store.Upsert(ctx, &session); err != nil {
		log.Printf("[realtime-sessions] record %s session failed user=%s item=%s: %v", session.Provider, userID, session.ItemID, err)
	}
}

func (r *Registry) Remove(provider, userID string, update models.PlaybackProgressUpdate) {
	if r == nil || r.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.store.Delete(ctx, strings.ToLower(provider), userID, strings.ToLower(update.MediaType), strings.ToLower(update.ItemID)); err != nil {
		log.Printf("[realtime-sessions] remove %s session failed user=%s item=%s: %v", provider, userID, update.ItemID, err)
	}
}

func (r *Registry) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Sweep(ctx)
		}
	}
}

// Recover drains sessions left by a previous process before new playback
// updates are accepted. Unlike a normal sweep, recovery intentionally ignores
// the active-playback dashboard: every persisted row belongs to the old
// in-memory tracker and must be closed before the replacement tracker starts.
func (r *Registry) Recover(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}
	r.mu.Lock()
	r.recoveryPending = true
	r.mu.Unlock()
	r.sweep(ctx, true)
}

func (r *Registry) Sweep(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}
	r.mu.RLock()
	recoverAll := r.recoveryPending
	r.mu.RUnlock()
	r.sweep(ctx, recoverAll)
}

func (r *Registry) sweep(ctx context.Context, recoverAll bool) {
	if r == nil || r.store == nil {
		return
	}
	sessions, err := r.store.List(ctx)
	if err != nil {
		log.Printf("[realtime-sessions] list failed: %v", err)
		return
	}
	if recoverAll {
		r.mu.Lock()
		for _, session := range sessions {
			r.blocked[sessionRecordKey(session.Provider, session.UserID, session.MediaType, session.ItemID)] = true
		}
		r.recoveryPending = false
		r.mu.Unlock()
	}
	for _, session := range sessions {
		r.mu.RLock()
		active := r.active
		cleaner := r.cleaners[session.Provider]
		blocked := r.blocked[sessionRecordKey(session.Provider, session.UserID, session.MediaType, session.ItemID)]
		r.mu.RUnlock()
		if !recoverAll && !blocked && (active == nil || active.IsPlaybackActive(session.UserID, session.Update)) {
			continue
		}
		if cleaner == nil {
			log.Printf("[realtime-sessions] no cleaner registered for provider %s", session.Provider)
			continue
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := cleaner.CleanupRealtimeSession(cleanupCtx, session)
		cancel()
		if err != nil {
			log.Printf("[realtime-sessions] cleanup failed provider=%s user=%s item=%s: %v", session.Provider, session.UserID, session.ItemID, err)
			continue
		}
		if err := r.store.Delete(ctx, session.Provider, session.UserID, session.MediaType, session.ItemID); err != nil {
			log.Printf("[realtime-sessions] delete cleaned record failed provider=%s user=%s item=%s: %v", session.Provider, session.UserID, session.ItemID, err)
			continue
		}
		r.mu.Lock()
		delete(r.blocked, sessionRecordKey(session.Provider, session.UserID, session.MediaType, session.ItemID))
		r.mu.Unlock()
		log.Printf("[realtime-sessions] removed lingering %s session user=%s item=%s", session.Provider, session.UserID, session.ItemID)
	}
}

// MemoryStore keeps legacy/non-PostgreSQL startup paths working.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]models.RealtimeScrobbleSession
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]models.RealtimeScrobbleSession)}
}

func sessionRecordKey(provider, userID, mediaType, itemID string) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(provider)), userID,
		strings.ToLower(strings.TrimSpace(mediaType)), strings.ToLower(strings.TrimSpace(itemID)),
	}, "\x00")
}

func (s *MemoryStore) Upsert(_ context.Context, session *models.RealtimeScrobbleSession) error {
	if session == nil {
		return fmt.Errorf("session is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionRecordKey(session.Provider, session.UserID, session.MediaType, session.ItemID)
	if existing, ok := s.sessions[key]; ok {
		session.StartedAt = existing.StartedAt
	}
	session.UpdatedAt = time.Now().UTC()
	s.sessions[key] = *session
	return nil
}

func (s *MemoryStore) List(_ context.Context) ([]models.RealtimeScrobbleSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]models.RealtimeScrobbleSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		result = append(result, session)
	}
	return result, nil
}

func (s *MemoryStore) Delete(_ context.Context, provider, userID, mediaType, itemID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionRecordKey(provider, userID, mediaType, itemID))
	return nil
}
