package profileunlock

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/models"
)

func newTestService(t *testing.T, store Store) (*Service, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	svc := NewService(store)
	svc.now = func() time.Time { return now }
	return svc, &now
}

func issue(t *testing.T, svc *Service, session, profile, fingerprint string, ttl time.Duration, sessionExpiry time.Time) (string, time.Time) {
	t.Helper()
	token, expiresAt, err := svc.Issue(IssueRequest{
		SessionToken:     session,
		SessionExpiresAt: sessionExpiry,
		AccountID:        "acct",
		ProfileID:        profile,
		PinFingerprint:   fingerprint,
		TTL:              ttl,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return token, expiresAt
}

func TestIssueAndValidate(t *testing.T) {
	svc, now := newTestService(t, nil)
	sessionExpiry := now.Add(30 * 24 * time.Hour)
	token, expiresAt := issue(t, svc, "session-a", "profile-1", "fp1", 12*time.Hour, sessionExpiry)

	if !strings.HasPrefix(token, TokenPrefix) || len(token) < 40 {
		t.Fatalf("unexpected token shape %q", token)
	}
	if want := now.Add(12 * time.Hour); !expiresAt.Equal(want) {
		t.Fatalf("expiresAt = %v, want %v", expiresAt, want)
	}
	if !svc.Validate([]string{token}, "session-a", "profile-1", "fp1") {
		t.Fatal("fresh token should validate")
	}
	// Any of several tokens may match.
	if !svc.Validate([]string{"pu_other", " " + token + " "}, "session-a", "profile-1", "fp1") {
		t.Fatal("token among several should validate")
	}

	for name, tc := range map[string]struct {
		tokens               []string
		session, profile, fp string
	}{
		"other session":   {[]string{token}, "session-b", "profile-1", "fp1"},
		"other profile":   {[]string{token}, "session-a", "profile-2", "fp1"},
		"pin changed":     {[]string{token}, "session-a", "profile-1", "fp2"},
		"pin removed":     {[]string{token}, "session-a", "profile-1", ""},
		"unknown token":   {[]string{"pu_nope"}, "session-a", "profile-1", "fp1"},
		"no token":        {nil, "session-a", "profile-1", "fp1"},
		"empty session":   {[]string{token}, "", "profile-1", "fp1"},
		"session as auth": {[]string{"session-a"}, "session-a", "profile-1", "fp1"},
	} {
		if svc.Validate(tc.tokens, tc.session, tc.profile, tc.fp) {
			t.Errorf("%s: should not validate", name)
		}
	}
}

func TestIssueRequiresSessionAndProfile(t *testing.T) {
	svc, _ := newTestService(t, nil)
	if _, _, err := svc.Issue(IssueRequest{ProfileID: "p"}); err != ErrSessionRequired {
		t.Fatalf("err = %v, want ErrSessionRequired", err)
	}
	if _, _, err := svc.Issue(IssueRequest{SessionToken: "s"}); err != ErrProfileRequired {
		t.Fatalf("err = %v, want ErrProfileRequired", err)
	}
}

func TestExpiry(t *testing.T) {
	svc, now := newTestService(t, nil)
	token, _ := issue(t, svc, "s", "p", "fp", time.Hour, now.Add(48*time.Hour))
	*now = now.Add(59 * time.Minute)
	if !svc.Validate([]string{token}, "s", "p", "fp") {
		t.Fatal("token should still be valid before expiry")
	}
	*now = now.Add(time.Minute)
	if svc.Validate([]string{token}, "s", "p", "fp") {
		t.Fatal("token must expire at expiresAt")
	}
	if removed := svc.Cleanup(); removed != 1 || svc.Count() != 0 {
		t.Fatalf("cleanup removed=%d count=%d", removed, svc.Count())
	}
}

func TestTTLIsCappedBySessionExpiry(t *testing.T) {
	svc, now := newTestService(t, nil)
	_, expiresAt := issue(t, svc, "s", "p", "fp", 12*time.Hour, now.Add(time.Hour))
	if want := now.Add(time.Hour); !expiresAt.Equal(want) {
		t.Fatalf("expiresAt = %v, want session expiry %v", expiresAt, want)
	}
}

func TestSessionBoundLifetimeFollowsRefresh(t *testing.T) {
	svc, now := newTestService(t, nil)
	firstExpiry := now.Add(time.Hour)
	token, expiresAt := issue(t, svc, "old", "p", "fp", 0, firstExpiry)
	if !expiresAt.Equal(firstExpiry) {
		t.Fatalf("session-bound expiresAt = %v, want %v", expiresAt, firstExpiry)
	}
	newExpiry := now.Add(30 * 24 * time.Hour)
	svc.RebindSession("old", "new", newExpiry)
	*now = now.Add(2 * time.Hour)
	if !svc.Validate([]string{token}, "new", "p", "fp") {
		t.Fatal("session-bound unlock should follow the refreshed session expiry")
	}
}

func TestRebindSessionCarriesUnlocksOver(t *testing.T) {
	svc, now := newTestService(t, nil)
	token, _ := issue(t, svc, "old", "p", "fp", 12*time.Hour, now.Add(24*time.Hour))
	other, _ := issue(t, svc, "unrelated", "p", "fp", 12*time.Hour, now.Add(24*time.Hour))

	svc.RebindSession("old", "new", now.Add(30*24*time.Hour))
	if !svc.Validate([]string{token}, "new", "p", "fp") {
		t.Fatal("unlock must carry over to the refreshed session token")
	}
	if svc.Validate([]string{token}, "old", "p", "fp") {
		t.Fatal("unlock must no longer validate with the rotated-out token")
	}
	if !svc.Validate([]string{other}, "unrelated", "p", "fp") {
		t.Fatal("other sessions' unlocks must be untouched")
	}
}

func TestRevokeSessionAndProfile(t *testing.T) {
	svc, now := newTestService(t, nil)
	exp := now.Add(24 * time.Hour)
	a1, _ := issue(t, svc, "a", "p1", "fp", time.Hour, exp)
	a2, _ := issue(t, svc, "a", "p2", "fp", time.Hour, exp)
	b1, _ := issue(t, svc, "b", "p1", "fp", time.Hour, exp)

	svc.RevokeSession("a")
	if svc.Validate([]string{a1}, "a", "p1", "fp") || svc.Validate([]string{a2}, "a", "p2", "fp") {
		t.Fatal("logout must revoke every unlock of the session")
	}
	if !svc.Validate([]string{b1}, "b", "p1", "fp") {
		t.Fatal("other session must keep its unlock")
	}

	svc.RevokeProfile("p1")
	if svc.Validate([]string{b1}, "b", "p1", "fp") {
		t.Fatal("PIN change must revoke the profile's unlocks")
	}
}

type memoryStore struct {
	mu     sync.Mutex
	grants map[string]models.ProfileUnlock
}

func (m *memoryStore) Create(_ context.Context, u *models.ProfileUnlock) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants[u.TokenHash] = *u
	return nil
}

func (m *memoryStore) List(context.Context) ([]models.ProfileUnlock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.ProfileUnlock
	for _, g := range m.grants {
		out = append(out, g)
	}
	return out, nil
}

func (m *memoryStore) DeleteBySession(_ context.Context, sessionHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, g := range m.grants {
		if g.SessionHash == sessionHash {
			delete(m.grants, k)
		}
	}
	return nil
}

func (m *memoryStore) DeleteByProfile(_ context.Context, profileID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, g := range m.grants {
		if g.ProfileID == profileID {
			delete(m.grants, k)
		}
	}
	return nil
}

func (m *memoryStore) RebindSession(_ context.Context, oldHash, newHash string, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, g := range m.grants {
		if g.SessionHash == oldHash {
			g.SessionHash = newHash
			if g.SessionBound {
				g.ExpiresAt = exp
			}
			m.grants[k] = g
		}
	}
	return nil
}

func (m *memoryStore) DeleteExpired(context.Context) (int64, error) { return 0, nil }

func TestPersistedUnlocksSurviveRestart(t *testing.T) {
	store := &memoryStore{grants: map[string]models.ProfileUnlock{}}
	svc, now := newTestService(t, store)
	token, _ := issue(t, svc, "old", "p", "fp", time.Hour, now.Add(24*time.Hour))
	svc.RebindSession("old", "new", now.Add(48*time.Hour))

	for hash, grant := range store.grants {
		if hash == token || grant.SessionHash == "new" || grant.SessionHash == "old" {
			t.Fatal("store must only hold hashes, never raw tokens")
		}
	}

	restarted := NewService(store)
	restarted.now = svc.now
	if !restarted.Validate([]string{token}, "new", "p", "fp") {
		t.Fatal("persisted, rebound unlock should validate after restart")
	}
	restarted.RevokeSession("new")
	if len(store.grants) != 0 {
		t.Fatalf("logout must delete persisted unlocks, %d left", len(store.grants))
	}
}
