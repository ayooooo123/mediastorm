package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sort"
	"sync"
	"time"

	"novastream/models"
)

const (
	// shareTokenBytes is the number of random bytes in a share link token.
	shareTokenBytes = 32

	// DefaultShareLinkTTL is used when a create request omits a validity period.
	DefaultShareLinkTTL = 24 * time.Hour

	// MaxShareLinkTTL caps how far in the future a share link may be valid.
	MaxShareLinkTTL = 7 * 24 * time.Hour

	// MaxShareLinkUses is the hard cap on how many times a share link may be
	// opened. There is no "unlimited" tier: a request for 0/unlimited, or for a
	// count above this cap, is clamped to MaxShareLinkUses.
	MaxShareLinkUses = models.ShareLinkMaxUses

	// shareCleanupInterval is how often the janitor purges expired share links.
	shareCleanupInterval = 30 * time.Minute
)

// ShareLinkRepo persists share links. It is satisfied by the Postgres repository
// and by an in-memory fallback used when no datastore is configured.
type ShareLinkRepo interface {
	Create(ctx context.Context, link *models.ShareLink) error
	Get(ctx context.Context, token string) (*models.ShareLink, error)
	ListAll(ctx context.Context) ([]models.ShareLink, error)
	ConsumeUse(ctx context.Context, token string, now time.Time) (*models.ShareLink, error)
	SetActive(ctx context.Context, token string, active bool) error
	Delete(ctx context.Context, token string) error
	DeleteExpired(ctx context.Context, now time.Time) error
}

// ShareStore mints and manages shareable playback links. Persistence is delegated
// to a ShareLinkRepo (Postgres in production; in-memory when no datastore exists).
type ShareStore struct {
	repo ShareLinkRepo
}

// NewShareStore creates a ShareStore backed by the given repository, starting a
// background janitor that purges expired links. A nil repo falls back to an
// in-memory store (links do not survive a restart and are not listable across
// processes — acceptable only when running without a datastore).
func NewShareStore(repo ShareLinkRepo) *ShareStore {
	if repo == nil {
		repo = newInMemoryShareRepo()
	}
	s := &ShareStore{repo: repo}
	go s.cleanupLoop()
	return s
}

// Create stores a new share link for the captured params and returns it. ttl is
// clamped to (0, MaxShareLinkTTL]; maxUses is clamped to (0, MaxShareLinkUses]
// — a request for 0/unlimited or above the cap becomes MaxShareLinkUses.
func (s *ShareStore) Create(ctx context.Context, accountID string, isMaster bool, params map[string]string, ttl time.Duration, maxUses int, label string) (*models.ShareLink, error) {
	token, err := generateShareToken()
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = DefaultShareLinkTTL
	}
	if ttl > MaxShareLinkTTL {
		ttl = MaxShareLinkTTL
	}
	if maxUses <= 0 || maxUses > MaxShareLinkUses {
		maxUses = MaxShareLinkUses
	}

	now := time.Now().UTC()
	link := &models.ShareLink{
		Token:     token,
		AccountID: accountID,
		IsMaster:  isMaster,
		Label:     label,
		Params:    params,
		MaxUses:   maxUses,
		Active:    true,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := s.repo.Create(ctx, link); err != nil {
		return nil, err
	}
	return link, nil
}

// Consume atomically records one use of a share link, returning it only if the
// link is active, unexpired, and under its use cap (single-use links allow one).
func (s *ShareStore) Consume(ctx context.Context, token string) (*models.ShareLink, bool) {
	if token == "" {
		return nil, false
	}
	link, err := s.repo.ConsumeUse(ctx, token, time.Now().UTC())
	if err != nil || link == nil {
		return nil, false
	}
	return link, true
}

// List returns all share links visible to the caller: every link for the master
// account, or only the caller's own links otherwise.
func (s *ShareStore) List(ctx context.Context, accountID string, isMaster bool) ([]models.ShareLink, error) {
	all, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if isMaster {
		return all, nil
	}
	owned := make([]models.ShareLink, 0, len(all))
	for _, link := range all {
		if link.AccountID == accountID {
			owned = append(owned, link)
		}
	}
	return owned, nil
}

// get returns a link if the caller is allowed to manage it.
func (s *ShareStore) get(ctx context.Context, accountID string, isMaster bool, token string) (*models.ShareLink, error) {
	link, err := s.repo.Get(ctx, token)
	if err != nil || link == nil {
		return nil, err
	}
	if !isMaster && link.AccountID != accountID {
		return nil, nil
	}
	return link, nil
}

// SetActive activates or deactivates a link the caller is allowed to manage.
// Returns false if the link does not exist or is not owned by the caller.
func (s *ShareStore) SetActive(ctx context.Context, accountID string, isMaster bool, token string, active bool) (bool, error) {
	link, err := s.get(ctx, accountID, isMaster, token)
	if err != nil || link == nil {
		return false, err
	}
	if err := s.repo.SetActive(ctx, token, active); err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes a link the caller is allowed to manage. Returns false if the
// link does not exist or is not owned by the caller.
func (s *ShareStore) Delete(ctx context.Context, accountID string, isMaster bool, token string) (bool, error) {
	link, err := s.get(ctx, accountID, isMaster, token)
	if err != nil || link == nil {
		return false, err
	}
	if err := s.repo.Delete(ctx, token); err != nil {
		return false, err
	}
	return true, nil
}

// cleanupLoop periodically removes expired share links.
func (s *ShareStore) cleanupLoop() {
	ticker := time.NewTicker(shareCleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		_ = s.repo.DeleteExpired(context.Background(), time.Now().UTC())
	}
}

func generateShareToken() (string, error) {
	buf := make([]byte, shareTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// --- In-memory fallback (no datastore) -------------------------------------

type inMemoryShareRepo struct {
	mu    sync.Mutex
	links map[string]*models.ShareLink
}

func newInMemoryShareRepo() *inMemoryShareRepo {
	return &inMemoryShareRepo{links: make(map[string]*models.ShareLink)}
}

func (m *inMemoryShareRepo) Create(_ context.Context, link *models.ShareLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := *link
	m.links[link.Token] = &clone
	return nil
}

func (m *inMemoryShareRepo) Get(_ context.Context, token string) (*models.ShareLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[token]
	if !ok {
		return nil, nil
	}
	clone := *link
	return &clone, nil
}

func (m *inMemoryShareRepo) ListAll(_ context.Context) ([]models.ShareLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]models.ShareLink, 0, len(m.links))
	for _, link := range m.links {
		result = append(result, *link)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (m *inMemoryShareRepo) ConsumeUse(_ context.Context, token string, now time.Time) (*models.ShareLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[token]
	if !ok || !link.Active || now.After(link.ExpiresAt) || link.Exhausted() {
		return nil, nil
	}
	link.UseCount++
	link.LastUsedAt = &now
	clone := *link
	return &clone, nil
}

func (m *inMemoryShareRepo) SetActive(_ context.Context, token string, active bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if link, ok := m.links[token]; ok {
		link.Active = active
	}
	return nil
}

func (m *inMemoryShareRepo) Delete(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, token)
	return nil
}

func (m *inMemoryShareRepo) DeleteExpired(_ context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, link := range m.links {
		if now.After(link.ExpiresAt) {
			delete(m.links, token)
		}
	}
	return nil
}
