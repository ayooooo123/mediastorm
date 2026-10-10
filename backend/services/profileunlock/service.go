// Package profileunlock issues and validates server-side profile PIN unlock
// tokens (MEDIASTORM-515).
//
// A successful PIN verification issues an opaque unlock token that is bound to
// (a) the account session token that performed the verification and (b) the
// profile's current PIN. The token stops validating when:
//   - its own lifetime elapses (configurable; or the session lifetime),
//   - the session is logged out (explicit revocation) or otherwise no longer
//     the session presenting it,
//   - the profile's PIN is changed or removed (PIN fingerprint mismatch plus
//     explicit revocation).
//
// Session refresh rotates the account token; RebindSession carries all unlocks
// from the old token to the new one so users are not re-prompted.
//
// Only SHA-256 hashes of the unlock token and session token are stored.
package profileunlock

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

// TokenPrefix marks unlock tokens so they are recognisable in logs/headers.
const TokenPrefix = "pu_"

// DefaultTTL is used when the configured lifetime is unset.
const DefaultTTL = 12 * time.Hour

var (
	// ErrSessionRequired is returned when issuing without a session token.
	ErrSessionRequired = errors.New("session token required")
	// ErrProfileRequired is returned when issuing without a profile id.
	ErrProfileRequired = errors.New("profile id required")
)

// Store persists unlock grants. The PostgreSQL repository satisfies it; a nil
// store keeps grants in memory only.
type Store interface {
	Create(ctx context.Context, unlock *models.ProfileUnlock) error
	List(ctx context.Context) ([]models.ProfileUnlock, error)
	DeleteBySession(ctx context.Context, sessionHash string) error
	DeleteByProfile(ctx context.Context, profileID string) error
	RebindSession(ctx context.Context, oldSessionHash, newSessionHash string, sessionExpiresAt time.Time) error
	DeleteExpired(ctx context.Context) (int64, error)
}

// Service manages profile unlock grants.
type Service struct {
	mu     sync.RWMutex
	store  Store
	grants map[string]models.ProfileUnlock // keyed by token hash
	now    func() time.Time
}

// NewService creates a service. store may be nil for memory-only operation.
func NewService(store Store) *Service {
	s := &Service{
		store:  store,
		grants: make(map[string]models.ProfileUnlock),
		now:    func() time.Time { return time.Now().UTC() },
	}
	if store != nil {
		if existing, err := store.List(context.Background()); err != nil {
			log.Printf("[profile-unlock] failed to load persisted unlocks: %v", err)
		} else {
			for _, grant := range existing {
				s.grants[grant.TokenHash] = grant
			}
		}
	}
	return s
}

// StartCleanup periodically removes expired grants until ctx is done.
func (s *Service) StartCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Cleanup()
			}
		}
	}()
}

// HashToken returns the hex SHA-256 of a token (unlock or session).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueRequest describes a new unlock grant.
type IssueRequest struct {
	SessionToken     string
	SessionExpiresAt time.Time
	AccountID        string
	ProfileID        string
	PinFingerprint   string
	// TTL > 0 gives the grant its own lifetime (capped by the session expiry).
	// TTL <= 0 makes the grant live as long as the session lineage.
	TTL time.Duration
}

// Issue creates a new unlock token and returns it with its expiry.
func (s *Service) Issue(req IssueRequest) (string, time.Time, error) {
	if strings.TrimSpace(req.SessionToken) == "" {
		return "", time.Time{}, ErrSessionRequired
	}
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		return "", time.Time{}, ErrProfileRequired
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()

	sessionBound := req.TTL <= 0
	var expiresAt time.Time
	if sessionBound {
		expiresAt = req.SessionExpiresAt
		if expiresAt.IsZero() {
			expiresAt = now.Add(DefaultTTL)
		}
	} else {
		expiresAt = now.Add(req.TTL)
		if !req.SessionExpiresAt.IsZero() && req.SessionExpiresAt.Before(expiresAt) {
			expiresAt = req.SessionExpiresAt
		}
	}
	expiresAt = expiresAt.UTC()

	grant := models.ProfileUnlock{
		TokenHash:      HashToken(token),
		SessionHash:    HashToken(req.SessionToken),
		AccountID:      req.AccountID,
		ProfileID:      profileID,
		PinFingerprint: req.PinFingerprint,
		SessionBound:   sessionBound,
		ExpiresAt:      expiresAt,
		CreatedAt:      now,
	}
	if s.store != nil {
		if err := s.store.Create(context.Background(), &grant); err != nil {
			return "", time.Time{}, err
		}
	}
	s.mu.Lock()
	s.grants[grant.TokenHash] = grant
	s.mu.Unlock()
	return token, expiresAt, nil
}

// Validate reports whether any of the supplied unlock tokens grants access to
// profileID for the given session token and the profile's current PIN
// fingerprint.
func (s *Service) Validate(unlockTokens []string, sessionToken, profileID, pinFingerprint string) bool {
	if s == nil || sessionToken == "" || profileID == "" {
		return false
	}
	sessionHash := HashToken(sessionToken)
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, token := range unlockTokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		grant, ok := s.grants[HashToken(token)]
		if !ok {
			continue
		}
		if !now.Before(grant.ExpiresAt) {
			continue
		}
		if grant.ProfileID != profileID {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(grant.SessionHash), []byte(sessionHash)) != 1 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(grant.PinFingerprint), []byte(pinFingerprint)) != 1 {
			continue
		}
		return true
	}
	return false
}

// RevokeSession deletes every unlock issued to a session (logout).
func (s *Service) RevokeSession(sessionToken string) {
	if s == nil || sessionToken == "" {
		return
	}
	sessionHash := HashToken(sessionToken)
	s.mu.Lock()
	for key, grant := range s.grants {
		if grant.SessionHash == sessionHash {
			delete(s.grants, key)
		}
	}
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.DeleteBySession(context.Background(), sessionHash); err != nil {
			log.Printf("[profile-unlock] revoke session: %v", err)
		}
	}
}

// RevokeProfile deletes every unlock for a profile (PIN changed or removed).
func (s *Service) RevokeProfile(profileID string) {
	profileID = strings.TrimSpace(profileID)
	if s == nil || profileID == "" {
		return
	}
	s.mu.Lock()
	for key, grant := range s.grants {
		if grant.ProfileID == profileID {
			delete(s.grants, key)
		}
	}
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.DeleteByProfile(context.Background(), profileID); err != nil {
			log.Printf("[profile-unlock] revoke profile: %v", err)
		}
	}
}

// RebindSession carries unlocks from a rotated session token to its
// replacement. Session-bound grants adopt the new session expiry.
func (s *Service) RebindSession(oldSessionToken, newSessionToken string, newSessionExpiresAt time.Time) {
	if s == nil || oldSessionToken == "" || newSessionToken == "" || oldSessionToken == newSessionToken {
		return
	}
	oldHash := HashToken(oldSessionToken)
	newHash := HashToken(newSessionToken)
	s.mu.Lock()
	for key, grant := range s.grants {
		if grant.SessionHash != oldHash {
			continue
		}
		grant.SessionHash = newHash
		if grant.SessionBound && !newSessionExpiresAt.IsZero() {
			grant.ExpiresAt = newSessionExpiresAt.UTC()
		}
		s.grants[key] = grant
	}
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.RebindSession(context.Background(), oldHash, newHash, newSessionExpiresAt.UTC()); err != nil {
			log.Printf("[profile-unlock] rebind session: %v", err)
		}
	}
}

// Cleanup removes expired grants and returns how many were dropped from memory.
func (s *Service) Cleanup() int {
	now := s.now()
	removed := 0
	s.mu.Lock()
	for key, grant := range s.grants {
		if !now.Before(grant.ExpiresAt) {
			delete(s.grants, key)
			removed++
		}
	}
	s.mu.Unlock()
	if s.store != nil {
		if _, err := s.store.DeleteExpired(context.Background()); err != nil {
			log.Printf("[profile-unlock] cleanup: %v", err)
		}
	}
	return removed
}

// Count returns the number of grants held in memory.
func (s *Service) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.grants)
}
