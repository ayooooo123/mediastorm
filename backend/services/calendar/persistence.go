package calendar

import (
	"context"
	"log"
	"time"

	"novastream/models"
)

const (
	kindFull = "full"
	kindLite = "lite"

	persistTimeout = 15 * time.Second
	// persistedMaxAge bounds how old a persisted snapshot may be to be served
	// as current; older ones are only kept as a stale Home fallback.
	persistedMaxAge = 7 * 24 * time.Hour
)

// Store persists built calendars so a restart can serve them immediately.
type Store interface {
	List(ctx context.Context) ([]models.UserCalendarSnapshot, error)
	Put(ctx context.Context, snap models.UserCalendarSnapshot) error
	MarkStale(ctx context.Context, userID string) error
}

// SetStore enables persistence of every successful build.
func (s *Service) SetStore(store Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

// LoadPersisted seeds the memory caches from the store. Call it before
// StartBackgroundRefresh so the initial population can skip fresh profiles.
// Entries already built in memory are never overwritten.
func (s *Service) LoadPersisted(ctx context.Context) int {
	s.mu.RLock()
	store := s.store
	s.mu.RUnlock()
	if store == nil {
		return 0
	}
	snaps, err := store.List(ctx)
	if err != nil {
		log.Printf("[calendar] load persisted calendars failed: %v", err)
		return 0
	}
	loaded := 0
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, snap := range snaps {
		if snap.UserID == "" {
			continue
		}
		uc := &userCalendar{Items: snap.Items, RefreshedAt: snap.RefreshedAt.UTC()}
		if snap.Stale || now.Sub(snap.RefreshedAt) > persistedMaxAge {
			// Prefer a full snapshot as the stale fallback.
			if prev := s.stale[snap.UserID]; prev == nil || snap.Kind == kindFull {
				s.stale[snap.UserID] = uc
			}
			continue
		}
		target := s.cache
		if snap.Kind == kindLite {
			target = s.liteCache
		} else if snap.Kind != kindFull {
			continue
		}
		if target[snap.UserID] != nil {
			continue
		}
		target[snap.UserID] = uc
		loaded++
	}
	log.Printf("[calendar] loaded %d persisted calendars (%d rows)", loaded, len(snaps))
	return loaded
}

func (s *Service) persist(store Store, userID, kind string, uc *userCalendar) {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	err := store.Put(ctx, models.UserCalendarSnapshot{
		UserID:      userID,
		Kind:        kind,
		Items:       uc.Items,
		RefreshedAt: uc.RefreshedAt,
	})
	if err != nil {
		log.Printf("[calendar] persist failed user=%s kind=%s: %v", userID, kind, err)
	}
}

func (s *Service) markPersistedStale(store Store, userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	if err := store.MarkStale(ctx, userID); err != nil {
		log.Printf("[calendar] mark persisted stale failed user=%s: %v", userID, err)
	}
}

// usersNeedingRefresh lists profiles to rebuild. With skipFresherThan > 0 it
// drops profiles whose entry in cache is younger than that, so a restart with
// freshly persisted calendars does not rebuild everyone.
func (s *Service) usersNeedingRefresh(cache map[string]*userCalendar, skipFresherThan time.Duration, tag string) []models.User {
	all := s.users.List()
	if skipFresherThan <= 0 || len(all) == 0 {
		return all
	}
	now := time.Now()
	out := make([]models.User, 0, len(all))
	s.mu.RLock()
	for _, u := range all {
		if uc := cache[u.ID]; uc != nil && now.Sub(uc.RefreshedAt) < skipFresherThan {
			continue
		}
		out = append(out, u)
	}
	s.mu.RUnlock()
	if skipped := len(all) - len(out); skipped > 0 {
		log.Printf("[%s] skipping %d/%d profiles with a calendar fresher than %s", tag, skipped, len(all), skipFresherThan)
	}
	return out
}
