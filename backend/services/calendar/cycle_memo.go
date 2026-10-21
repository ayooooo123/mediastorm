package calendar

import (
	"context"
	"fmt"
	"sync"

	"novastream/models"
)

// cycleMetadata memoizes the user-independent metadata a refresh cycle needs
// (trending lists, MDBList shelf items, series/movie details) so each profile
// build reuses what an earlier profile already fetched. It lives for exactly
// one refresh cycle; per-user filtering and dedupe stay in buildState.
// Concurrent callers for the same key share one in-flight fetch; only
// successful results are kept, so a transient failure is retried by the next
// profile as before.
type cycleMetadata struct {
	inner MetadataService

	mu      sync.Mutex
	entries map[string]*memoEntry
}

type memoEntry struct {
	done  chan struct{}
	value any
	err   error
}

func newCycleMetadata(inner MetadataService) *cycleMetadata {
	return &cycleMetadata{inner: inner, entries: make(map[string]*memoEntry)}
}

func (m *cycleMetadata) do(key string, fetch func() (any, error)) (any, error) {
	m.mu.Lock()
	if e, ok := m.entries[key]; ok {
		m.mu.Unlock()
		<-e.done
		if e.err == nil {
			return e.value, nil
		}
		// The shared attempt failed; fall through to a fresh attempt below.
		m.mu.Lock()
		if cur, ok := m.entries[key]; ok && cur != e {
			m.mu.Unlock()
			<-cur.done
			return cur.value, cur.err
		}
	}
	e := &memoEntry{done: make(chan struct{})}
	m.entries[key] = e
	m.mu.Unlock()

	e.value, e.err = fetch()
	if e.err != nil {
		m.mu.Lock()
		if m.entries[key] == e {
			delete(m.entries, key)
		}
		m.mu.Unlock()
	}
	close(e.done)
	return e.value, e.err
}

func (m *cycleMetadata) SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return m.inner.SeriesDetails(ctx, req)
}

func (m *cycleMetadata) SeriesDetailsLite(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	v, err := m.do(fmt.Sprintf("series-lite|%+v", req), func() (any, error) {
		return m.inner.SeriesDetailsLite(ctx, req)
	})
	details, _ := v.(*models.SeriesDetails)
	return details, err
}

func (m *cycleMetadata) MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	v, err := m.do(fmt.Sprintf("movie|%+v", req), func() (any, error) {
		return m.inner.MovieDetails(ctx, req)
	})
	title, _ := v.(*models.Title)
	return title, err
}

func (m *cycleMetadata) Trending(ctx context.Context, mediaType string) ([]models.TrendingItem, error) {
	v, err := m.do("trending|"+mediaType, func() (any, error) {
		return m.inner.Trending(ctx, mediaType)
	})
	items, _ := v.([]models.TrendingItem)
	return items, err
}

func (m *cycleMetadata) GetCustomListForCalendar(ctx context.Context, listURL string, limit int, label string) ([]models.TrendingItem, error) {
	v, err := m.do(fmt.Sprintf("mdblist|%s|%d|%s", listURL, limit, label), func() (any, error) {
		return m.inner.GetCustomListForCalendar(ctx, listURL, limit, label)
	})
	items, _ := v.([]models.TrendingItem)
	return items, err
}

// md returns the metadata source for a build: the cycle memo when present.
func (s *Service) md(state *buildState) MetadataService {
	if state != nil && state.md != nil {
		return state.md
	}
	return s.metadata
}
