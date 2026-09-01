package remoteaccess

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

type fakeInviteRepo struct {
	byID   map[string]models.RemoteAccessInvite
	byHash map[string]string
}

type fakePairingRepo struct {
	byID   map[string]models.RemoteAccessPairing
	byPeer map[string]string
}

func newFakePairingRepo() *fakePairingRepo {
	return &fakePairingRepo{byID: make(map[string]models.RemoteAccessPairing), byPeer: make(map[string]string)}
}

func (r *fakePairingRepo) Get(_ context.Context, id string) (*models.RemoteAccessPairing, error) {
	pairing, ok := r.byID[id]
	if !ok {
		return nil, nil
	}
	return &pairing, nil
}

func (r *fakePairingRepo) GetByPeerID(ctx context.Context, peerID string) (*models.RemoteAccessPairing, error) {
	return r.Get(ctx, r.byPeer[peerID])
}

func (r *fakePairingRepo) List(context.Context) ([]models.RemoteAccessPairing, error) {
	result := make([]models.RemoteAccessPairing, 0, len(r.byID))
	for _, pairing := range r.byID {
		result = append(result, pairing)
	}
	return result, nil
}

func (r *fakePairingRepo) Create(_ context.Context, pairing *models.RemoteAccessPairing) error {
	r.byID[pairing.ID] = *pairing
	r.byPeer[pairing.PeerID] = pairing.ID
	return nil
}

func (r *fakePairingRepo) Update(ctx context.Context, pairing *models.RemoteAccessPairing) error {
	return r.Create(ctx, pairing)
}

func (r *fakePairingRepo) Delete(_ context.Context, id string) error {
	if pairing, ok := r.byID[id]; ok {
		delete(r.byPeer, pairing.PeerID)
	}
	delete(r.byID, id)
	return nil
}

func (r *fakePairingRepo) Count(context.Context) (int64, error) { return int64(len(r.byID)), nil }

type fakeHost struct {
	invite         string
	running        bool
	ensures        int
	stops          int
	publishedCodes []string
}

func newFakeInviteRepo() *fakeInviteRepo {
	return &fakeInviteRepo{
		byID:   make(map[string]models.RemoteAccessInvite),
		byHash: make(map[string]string),
	}
}

func (h *fakeHost) Ensure(ctx context.Context) (string, error) {
	h.ensures++
	h.running = true
	if h.invite == "" {
		h.invite = "mshost-iroh-direct-test"
	}
	return h.invite, nil
}

func (h *fakeHost) Stop(ctx context.Context) error {
	h.stops++
	h.running = false
	return nil
}

func (h *fakeHost) Status(ctx context.Context) models.RemoteAccessStatus {
	return models.RemoteAccessStatus{
		Enabled:     true,
		Running:     h.running,
		Provider:    "iroh",
		State:       "test",
		ActiveHosts: boolToInt(h.running),
	}
}

func (h *fakeHost) PublishRendezvousRecords(ctx context.Context, codes []string, invite string) error {
	h.publishedCodes = append(h.publishedCodes, codes...)
	return nil
}

func (r *fakeInviteRepo) Get(ctx context.Context, id string) (*models.RemoteAccessInvite, error) {
	inv, ok := r.byID[id]
	if !ok {
		return nil, nil
	}
	return &inv, nil
}

func (r *fakeInviteRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*models.RemoteAccessInvite, error) {
	id, ok := r.byHash[tokenHash]
	if !ok {
		return nil, nil
	}
	return r.Get(ctx, id)
}

func (r *fakeInviteRepo) List(ctx context.Context) ([]models.RemoteAccessInvite, error) {
	result := make([]models.RemoteAccessInvite, 0, len(r.byID))
	for _, inv := range r.byID {
		result = append(result, inv)
	}
	return result, nil
}

func (r *fakeInviteRepo) Create(ctx context.Context, inv *models.RemoteAccessInvite) error {
	r.byID[inv.ID] = *inv
	r.byHash[inv.TokenHash] = inv.ID
	return nil
}

func (r *fakeInviteRepo) ClaimByTokenHash(ctx context.Context, tokenHash string, peerID string, now time.Time) (*models.RemoteAccessInvite, error) {
	inv, err := r.GetByTokenHash(ctx, tokenHash)
	if err != nil || inv == nil {
		return inv, err
	}
	if inv.RevokedAt != nil || (!now.Before(inv.ExpiresAt) && inv.UsedByPeerID != peerID) {
		return nil, nil
	}
	if inv.UsedAt != nil && inv.UsedByPeerID != peerID {
		return nil, nil
	}
	if inv.UsedAt == nil {
		inv.UsedAt = &now
		inv.UsedByPeerID = peerID
		if err := r.Update(ctx, inv); err != nil {
			return nil, err
		}
	}
	return inv, nil
}

func (r *fakeInviteRepo) Update(ctx context.Context, inv *models.RemoteAccessInvite) error {
	r.byID[inv.ID] = *inv
	r.byHash[inv.TokenHash] = inv.ID
	return nil
}

func (r *fakeInviteRepo) Delete(ctx context.Context, id string) error {
	inv, ok := r.byID[id]
	if ok {
		delete(r.byHash, inv.TokenHash)
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeInviteRepo) Count(ctx context.Context) (int64, error) {
	return int64(len(r.byID)), nil
}

func TestRevokePeerRevokesEveryClaimedInviteForDevice(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{}
	svc := NewService(repo, host)
	now := time.Now().UTC()
	for _, inv := range []models.RemoteAccessInvite{
		{ID: "one", UsedAt: &now, UsedByPeerID: "device-1", ExpiresAt: now.Add(time.Hour)},
		{ID: "two", UsedAt: &now, UsedByPeerID: "device-1", ExpiresAt: now.Add(time.Hour)},
		{ID: "other", UsedAt: &now, UsedByPeerID: "device-2", ExpiresAt: now.Add(time.Hour)},
	} {
		copy := inv
		repo.byID[inv.ID] = copy
	}
	authorized, err := svc.IsPeerAuthorized(context.Background(), "device-1")
	if err != nil || !authorized {
		t.Fatalf("IsPeerAuthorized() before revoke = %v, %v; want true, nil", authorized, err)
	}
	count, err := svc.RevokePeer(context.Background(), "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("RevokePeer() = %d, want 2", count)
	}
	if repo.byID["one"].RevokedAt == nil || repo.byID["two"].RevokedAt == nil {
		t.Fatal("matching claimed invites were not revoked")
	}
	if repo.byID["other"].RevokedAt != nil {
		t.Fatal("unrelated device invite was revoked")
	}
	revoked, err := svc.IsPeerRevoked(context.Background(), "device-1")
	if err != nil || !revoked {
		t.Fatalf("IsPeerRevoked() = %v, %v; want true, nil", revoked, err)
	}
	otherRevoked, err := svc.IsPeerRevoked(context.Background(), "device-2")
	if err != nil || otherRevoked {
		t.Fatalf("other IsPeerRevoked() = %v, %v; want false, nil", otherRevoked, err)
	}
	authorized, err = svc.IsPeerAuthorized(context.Background(), "device-1")
	if err != nil || authorized {
		t.Fatalf("IsPeerAuthorized() after revoke = %v, %v; want false, nil", authorized, err)
	}
	otherAuthorized, err := svc.IsPeerAuthorized(context.Background(), "device-2")
	if err != nil || !otherAuthorized {
		t.Fatalf("other IsPeerAuthorized() = %v, %v; want true, nil", otherAuthorized, err)
	}
}

func TestCredentialPairingRetiresInviteSecretAndAuthorizesOnlyMatchingSecret(t *testing.T) {
	invites := newFakeInviteRepo()
	pairings := newFakePairingRepo()
	svc := NewService(invites, &fakeHost{}, pairings)
	credential := strings.Repeat("a", 43)

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimInvite(context.Background(), inv.Token, "device-1", credential)
	if err != nil {
		t.Fatal(err)
	}
	stored := invites.byID[claimed.ID]
	if stored.ConnectionCode != "" || stored.TokenHash != "claimed:"+claimed.ID {
		t.Fatalf("claimed invite retained secret material: %+v", stored)
	}
	pairing, err := pairings.GetByPeerID(context.Background(), "device-1")
	if err != nil || pairing == nil || pairing.CredentialHash == "" {
		t.Fatalf("pairing = %+v, err = %v", pairing, err)
	}
	if authorized, err := svc.AuthorizePeer(context.Background(), "device-1", credential); err != nil || !authorized {
		t.Fatalf("matching credential authorized=%v err=%v", authorized, err)
	}
	if authorized, err := svc.AuthorizePeer(context.Background(), "device-1", strings.Repeat("b", 43)); err != nil || authorized {
		t.Fatalf("wrong credential authorized=%v err=%v", authorized, err)
	}

	// The staged client can retry after the claim response is lost even though the
	// one-time token has already been scrubbed.
	retry, err := svc.ClaimInvite(context.Background(), inv.Token, "device-1", credential)
	if err != nil || retry.ID != claimed.ID {
		t.Fatalf("idempotent credential retry = %+v, %v", retry, err)
	}
	if err := svc.RevokeInvite(context.Background(), claimed.ID); err != nil {
		t.Fatal(err)
	}
	if authorized, _ := svc.AuthorizePeer(context.Background(), "device-1", credential); authorized {
		t.Fatal("revoking the pairing's invite left the device authorized")
	}
}

func TestNewPairingRequiresDeviceCredential(t *testing.T) {
	invites := newFakeInviteRepo()
	pairings := newFakePairingRepo()
	svc := NewService(invites, &fakeHost{}, pairings)
	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "device-1"); err != ErrInvalidPairingCredential {
		t.Fatalf("ClaimInvite() error = %v, want ErrInvalidPairingCredential", err)
	}
	stored := invites.byID[inv.ID]
	if stored.UsedAt != nil {
		t.Fatal("invite was consumed without a device credential")
	}
}

func TestLegacyPairingCanUpgradeOnceToCredential(t *testing.T) {
	pairings := newFakePairingRepo()
	pairing := &models.RemoteAccessPairing{ID: "legacy-1", PeerID: "device-1", CreatedBy: "account-1"}
	if err := pairings.Create(context.Background(), pairing); err != nil {
		t.Fatal(err)
	}
	svc := NewService(newFakeInviteRepo(), &fakeHost{}, pairings)
	if authorized, err := svc.AuthorizePeer(context.Background(), "device-1", ""); err != nil || !authorized {
		t.Fatalf("legacy authorization = %v, %v", authorized, err)
	}
	credential := strings.Repeat("c", 43)
	if err := svc.UpgradePairingCredential(context.Background(), "device-1", credential); err != nil {
		t.Fatal(err)
	}
	if authorized, _ := svc.AuthorizePeer(context.Background(), "device-1", ""); authorized {
		t.Fatal("upgraded pairing still accepts peer ID without credential")
	}
	if authorized, _ := svc.AuthorizePeer(context.Background(), "device-1", credential); !authorized {
		t.Fatal("upgraded pairing rejected its credential")
	}
	if err := svc.UpgradePairingCredential(context.Background(), "device-1", strings.Repeat("d", 43)); err != ErrInvalidPairingCredential {
		t.Fatalf("credential replacement error = %v, want ErrInvalidPairingCredential", err)
	}
}

func TestClaimInviteRejectsMissingOrOversizedPeerID(t *testing.T) {
	repo := newFakeInviteRepo()
	svc := NewService(repo, &fakeHost{})

	for name, peerID := range map[string]string{
		"missing":   "   ",
		"oversized": strings.Repeat("x", 257),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.ClaimInvite(context.Background(), "mshost-ABCDEF-GHJKMN-PQRSTV", peerID); err != ErrInvalidPeerID {
				t.Fatalf("ClaimInvite() error = %v, want ErrInvalidPeerID", err)
			}
		})
	}
}

func TestCreateInviteStartsSharedIrohHost(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{}
	svc := NewService(repo, host)
	svc.now = func() time.Time { return time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC) }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{PeerName: "iPhone"})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if !host.running || host.ensures != 1 {
		t.Fatalf("host running=%t ensures=%d, want running with one ensure", host.running, host.ensures)
	}
	if inv.ConnectionCode == "" || inv.ConnectionCode == inv.IrohInvite {
		t.Fatalf("connection code = %q, iroh invite = %q; want separate short code and iroh invite", inv.ConnectionCode, inv.IrohInvite)
	}
	if inv.IrohInvite != "mshost-iroh-direct-test" {
		t.Fatalf("iroh invite = %q, want resolved iroh invite", inv.IrohInvite)
	}
	if len(host.publishedCodes) != 1 || host.publishedCodes[0] != inv.ConnectionCode {
		t.Fatalf("published codes = %v, want [%s]", host.publishedCodes, inv.ConnectionCode)
	}
}

func TestSuperviseStopsHostWhenNoActiveInvites(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{running: true, invite: "mshost-iroh-direct-test"}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	repo.byID[inv.ID] = stored

	summary, err := svc.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}
	if summary.Active != 0 || !summary.Stopped {
		t.Fatalf("summary = %+v, want stopped with zero active", summary)
	}
	if host.running || host.stops != 1 {
		t.Fatalf("host running=%t stops=%d, want stopped once", host.running, host.stops)
	}
}

func TestSuperviseKeepsHostAfterInviteClaim(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}

	summary, err := svc.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}
	if summary.Active != 1 || summary.Stopped {
		t.Fatalf("summary = %+v, want claimed invite to remain active", summary)
	}
	if !host.running {
		t.Fatal("expected host to keep running for claimed invite")
	}
}

func TestSuperviseKeepsHostAfterClaimedInviteExpires(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	repo.byID[inv.ID] = stored

	summary, err := svc.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}
	if summary.Active != 1 || summary.Stopped {
		t.Fatalf("summary = %+v, want expired claimed invite to remain active", summary)
	}
	if !host.running {
		t.Fatal("expected host to keep running for expired claimed invite")
	}
}

func TestResolveInviteRejectsClaimedInvite(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{invite: "mshost-iroh-direct-new"}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	stored.IrohInvite = "mshost-iroh-direct-old"
	repo.byID[inv.ID] = stored

	if _, err := svc.ResolveInvite(context.Background(), inv.Token); err != ErrInviteUsed {
		t.Fatalf("ResolveInvite error = %v, want ErrInviteUsed", err)
	}
}

func TestClaimInviteRetryIsIdempotentOnlyForTheSameDevice(t *testing.T) {
	repo := newFakeInviteRepo()
	svc := NewService(repo, &fakeHost{})
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	first, err := svc.ClaimInvite(context.Background(), inv.Token, "device-1")
	if err != nil {
		t.Fatalf("first ClaimInvite returned error: %v", err)
	}

	// Simulate a retry after the phone received the server response but failed to
	// persist its local connection state. The same stable device may recover.
	now = now.Add(2 * time.Hour)
	retry, err := svc.ClaimInvite(context.Background(), inv.Token, "device-1")
	if err != nil {
		t.Fatalf("same-device retry returned error: %v", err)
	}
	if retry.ID != first.ID || retry.UsedByPeerID != "device-1" {
		t.Fatalf("same-device retry = %+v, want original pairing", retry)
	}

	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "device-2"); err != ErrInviteExpired {
		t.Fatalf("different-device retry error = %v, want ErrInviteExpired", err)
	}
}

func TestResolveClaimedInviteForPeerRecoversConnectionCode(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{invite: "mshost-iroh-direct-new"}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	stored.IrohInvite = "mshost-iroh-direct-old"
	repo.byID[inv.ID] = stored

	resolved, err := svc.ResolveClaimedInviteForPeer(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("ResolveClaimedInviteForPeer returned error: %v", err)
	}
	if resolved.ConnectionCode != inv.Token {
		t.Fatalf("connection code = %q, want original short code", resolved.ConnectionCode)
	}
	if resolved.IrohInvite != "mshost-iroh-direct-new" {
		t.Fatalf("iroh invite = %q, want refreshed host invite", resolved.IrohInvite)
	}
}

// fakeRendezvousHost is a fakeHost that also advertises a rendezvous file path, so the
// service mirrors active connection codes into it.
type fakeRendezvousHost struct {
	fakeHost
	path string
}

func (h *fakeRendezvousHost) RendezvousFilePath() string { return h.path }

func readRendezvousCodes(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rendezvous file: %v", err)
	}
	var codes []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		codes = append(codes, line)
	}
	return codes
}

func TestCreateInviteWritesRendezvousFile(t *testing.T) {
	repo := newFakeInviteRepo()
	path := filepath.Join(t.TempDir(), "codes.txt")
	host := &fakeRendezvousHost{path: path}
	svc := NewService(repo, host)
	svc.now = func() time.Time { return time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC) }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}

	codes := readRendezvousCodes(t, path)
	if len(codes) != 1 || codes[0] != inv.ConnectionCode {
		t.Fatalf("rendezvous codes = %v, want [%s]", codes, inv.ConnectionCode)
	}
}

func TestGenerateTokenHasHighEntropyUnambiguousBody(t *testing.T) {
	const prefix = "mshost-"
	seen := make(map[string]struct{})
	for i := 0; i < 200; i++ {
		token, err := generateToken()
		if err != nil {
			t.Fatalf("generateToken returned error: %v", err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("generateToken produced a duplicate token %q", token)
		}
		seen[token] = struct{}{}

		if !strings.HasPrefix(token, prefix) {
			t.Fatalf("token %q missing %q prefix", token, prefix)
		}
		groups := strings.Split(strings.TrimPrefix(token, prefix), "-")
		if len(groups) != 3 {
			t.Fatalf("token %q body = %v, want three groups", token, groups)
		}
		body := strings.Join(groups, "")
		if len(body) != codeBodyLength {
			t.Fatalf("token %q body length = %d, want %d", token, len(body), codeBodyLength)
		}
		for _, c := range body {
			if !strings.ContainsRune(codeAlphabet, c) {
				t.Fatalf("token %q contains character %q outside the Crockford base32 alphabet", token, string(c))
			}
		}
		// Crockford base32 deliberately omits these ambiguous characters.
		if strings.ContainsAny(body, "ILOU") {
			t.Fatalf("token %q body contains an ambiguous character", token)
		}
	}
}

func TestClaimDropsConnectionCodeFromRendezvousFile(t *testing.T) {
	repo := newFakeInviteRepo()
	path := filepath.Join(t.TempDir(), "codes.txt")
	host := &fakeRendezvousHost{path: path}
	svc := NewService(repo, host)
	svc.now = func() time.Time { return time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC) }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if codes := readRendezvousCodes(t, path); len(codes) != 1 || codes[0] != inv.ConnectionCode {
		t.Fatalf("rendezvous codes before claim = %v, want [%s]", codes, inv.ConnectionCode)
	}

	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}

	// A claimed invite stays active (host keeps running for reconnects) but its code must
	// no longer be published to the DHT.
	if codes := readRendezvousCodes(t, path); len(codes) != 0 {
		t.Fatalf("rendezvous codes after claim = %v, want none", codes)
	}
}

func TestSuperviseEmptiesRendezvousFileWhenNoActiveInvites(t *testing.T) {
	repo := newFakeInviteRepo()
	path := filepath.Join(t.TempDir(), "codes.txt")
	host := &fakeRendezvousHost{path: path}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if codes := readRendezvousCodes(t, path); len(codes) != 1 {
		t.Fatalf("expected one active code after create, got %v", codes)
	}

	// Expire the invite, then supervise should rewrite the file with no codes.
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	repo.byID[inv.ID] = stored

	if _, err := svc.Supervise(context.Background()); err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}
	if codes := readRendezvousCodes(t, path); len(codes) != 0 {
		t.Fatalf("expected no active codes after expiry, got %v", codes)
	}
}
