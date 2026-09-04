package remoteaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrInviteNotFound           = errors.New("remote access invite not found")
	ErrInviteExpired            = errors.New("remote access invite has expired")
	ErrInviteUsed               = errors.New("remote access invite has already been used")
	ErrInviteRevoked            = errors.New("remote access invite has been revoked")
	ErrInvalidToken             = errors.New("invalid remote access invite token")
	ErrInvalidPeerID            = errors.New("invalid remote access peer id")
	ErrInvalidPairingCredential = errors.New("invalid remote access pairing credential")
)

const (
	DefaultInviteExpiration = 24 * time.Hour
	MaxInviteExpiration     = 30 * 24 * time.Hour
)

type HostManager interface {
	Ensure(ctx context.Context) (string, error)
	Stop(ctx context.Context) error
	Status(ctx context.Context) models.RemoteAccessStatus
}

// RendezvousPublisher is an optional capability implemented by hosts that publish
// connection codes to the public DHT, letting clients resolve an invite without a
// reachable backend URL. When the host implements it, the service mirrors the set of
// active connection codes into the returned file path whenever invites change, and the
// host watches that file to keep a DHT record live for each code.
type RendezvousPublisher interface {
	RendezvousFilePath() string
}

// RendezvousImmediatePublisher is implemented by hosts that can publish records on
// demand. The background host publisher still refreshes records; this fast path makes a
// freshly created code usable immediately and gives us clearer logging when publishing
// fails.
type RendezvousImmediatePublisher interface {
	PublishRendezvousRecords(ctx context.Context, codes []string, invite string) error
}

type CreateInviteRequest struct {
	PeerName  string
	ExpiresIn time.Duration
}

type SyncSummary struct {
	Active  int
	Started bool
	Stopped bool
	Updated int
}

type Service struct {
	invites  datastore.RemoteAccessInviteRepository
	pairings datastore.RemoteAccessPairingRepository
	host     HostManager
	now      func() time.Time
}

func NewService(invites datastore.RemoteAccessInviteRepository, host HostManager, pairings ...datastore.RemoteAccessPairingRepository) *Service {
	service := &Service{
		invites: invites,
		host:    host,
		now:     func() time.Time { return time.Now().UTC() },
	}
	if len(pairings) > 0 {
		service.pairings = pairings[0]
	}
	return service
}

func (s *Service) Status(ctx context.Context) models.RemoteAccessStatus {
	status := models.RemoteAccessStatus{
		Enabled:  false,
		Running:  false,
		Provider: "iroh",
		State:    "not_configured",
	}
	if s.host != nil {
		status = s.host.Status(ctx)
	}
	invites, err := s.invites.List(ctx)
	if err == nil {
		status.ActiveInvites = countActiveInvites(invites, s.now())
	}
	return status
}

func (s *Service) CreateInvite(ctx context.Context, createdBy string, req CreateInviteRequest) (models.RemoteAccessInvite, error) {
	createdBy = strings.TrimSpace(createdBy)
	if createdBy == "" {
		return models.RemoteAccessInvite{}, fmt.Errorf("created by account id is required")
	}
	if s.host == nil {
		return models.RemoteAccessInvite{}, errors.New("iroh host manager not configured")
	}
	expiresIn := req.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = DefaultInviteExpiration
	}
	if expiresIn > MaxInviteExpiration {
		expiresIn = MaxInviteExpiration
	}

	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	token, err := generateToken()
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	now := s.now()
	inv := models.RemoteAccessInvite{
		ID:             uuid.NewString(),
		TokenHash:      HashInviteToken(token),
		ConnectionCode: token,
		IrohInvite:     irohInvite,
		CreatedBy:      createdBy,
		PeerName:       strings.TrimSpace(req.PeerName),
		ExpiresAt:      now.Add(expiresIn),
		CreatedAt:      now,
	}
	if err := s.invites.Create(ctx, &inv); err != nil {
		return models.RemoteAccessInvite{}, err
	}
	// Best-effort: the host re-reads the file on a timer and Supervise rewrites it
	// every minute, so a transient failure here self-heals.
	s.trySyncRendezvousCodes(ctx)
	s.tryPublishRendezvousCodes(ctx, []string{inv.ConnectionCode}, irohInvite)
	inv.Token = token
	return inv, nil
}

func (s *Service) ListInvites(ctx context.Context) ([]models.RemoteAccessInvite, error) {
	invites, err := s.invites.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range invites {
		invites[i].Token = ""
	}
	return invites, nil
}

func (s *Service) RevokeInvite(ctx context.Context, id string) error {
	inv, err := s.invites.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if inv == nil {
		return ErrInviteNotFound
	}
	now := s.now()
	if s.pairings != nil && inv.UsedByPeerID != "" {
		pairing, err := s.pairings.GetByPeerID(ctx, inv.UsedByPeerID)
		if err != nil {
			return err
		}
		if pairing != nil && pairing.InviteID != nil && *pairing.InviteID == inv.ID && pairing.RevokedAt == nil {
			pairing.RevokedAt = &now
			if err := s.pairings.Update(ctx, pairing); err != nil {
				return err
			}
		}
	}
	inv.RevokedAt = &now
	if err := s.invites.Update(ctx, inv); err != nil {
		return err
	}
	_, err = s.Supervise(ctx)
	return err
}

// RevokePeer revokes every claimed invite associated with a paired device.
// A device can have more than one historical invite, so revoking only the row
// selected in the UI could otherwise leave another pairing active.
func (s *Service) RevokePeer(ctx context.Context, peerID string) (int, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return 0, ErrInviteNotFound
	}
	invites, err := s.invites.List(ctx)
	if err != nil {
		return 0, err
	}
	now := s.now()
	count := 0
	if s.pairings != nil {
		pairing, err := s.pairings.GetByPeerID(ctx, peerID)
		if err != nil {
			return 0, err
		}
		if pairing != nil && pairing.RevokedAt == nil {
			pairing.RevokedAt = &now
			if err := s.pairings.Update(ctx, pairing); err != nil {
				return 0, err
			}
		}
	}
	for i := range invites {
		inv := &invites[i]
		if inv.UsedAt == nil || inv.UsedByPeerID != peerID || inv.RevokedAt != nil {
			continue
		}
		inv.RevokedAt = &now
		if err := s.invites.Update(ctx, inv); err != nil {
			return count, err
		}
		count++
	}
	if count == 0 {
		return 0, nil
	}
	_, err = s.Supervise(ctx)
	return count, err
}

// IsPeerRevoked reports whether a device has claimed pairings but no remaining
// active pairing. It is used to reject requests arriving over the Iroh proxy
// after an administrator revokes device access.
func (s *Service) IsPeerRevoked(ctx context.Context, peerID string) (bool, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return false, nil
	}
	if s.pairings != nil {
		pairing, err := s.pairings.GetByPeerID(ctx, peerID)
		if err != nil {
			return false, err
		}
		return pairing != nil && pairing.RevokedAt != nil, nil
	}
	invites, err := s.invites.List(ctx)
	if err != nil {
		return false, err
	}
	hasRevoked := false
	for _, inv := range invites {
		if inv.UsedAt == nil || inv.UsedByPeerID != peerID {
			continue
		}
		if inv.RevokedAt == nil {
			return false, nil
		}
		hasRevoked = true
	}
	return hasRevoked, nil
}

// IsPeerAuthorized reports whether a device has at least one consumed,
// non-revoked pairing. Expiry only limits the initial claim; a consumed pairing
// remains valid until an administrator explicitly revokes it.
func (s *Service) IsPeerAuthorized(ctx context.Context, peerID string) (bool, error) {
	return s.AuthorizePeer(ctx, peerID, "")
}

// AuthorizePeer verifies the durable device pairing. Legacy rows created by the schema
// migration have no credential hash and temporarily retain peer-ID authorization until the
// updated client rotates them through UpgradePairingCredential.
func (s *Service) AuthorizePeer(ctx context.Context, peerID, credential string) (bool, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return false, nil
	}
	if s.pairings != nil {
		pairing, err := s.pairings.GetByPeerID(ctx, peerID)
		if err != nil {
			return false, err
		}
		if pairing == nil || pairing.RevokedAt != nil {
			return false, nil
		}
		if pairing.CredentialHash == "" {
			return true, nil
		}
		actual, err := hashPairingCredential(credential)
		if err != nil {
			return false, nil
		}
		return subtle.ConstantTimeCompare([]byte(pairing.CredentialHash), []byte(actual)) == 1, nil
	}
	invites, err := s.invites.List(ctx)
	if err != nil {
		return false, err
	}
	for _, inv := range invites {
		if inv.UsedAt != nil && inv.RevokedAt == nil && inv.UsedByPeerID == peerID {
			return true, nil
		}
	}
	return false, nil
}

// (Supervise rewrites the rendezvous file, so RevokeInvite relies on it via the call above.)

func (s *Service) ClaimInvite(ctx context.Context, token, peerID string, credentials ...string) (models.RemoteAccessInvite, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return models.RemoteAccessInvite{}, ErrInvalidToken
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" || len(peerID) > 256 {
		return models.RemoteAccessInvite{}, ErrInvalidPeerID
	}
	credential := ""
	if len(credentials) > 0 {
		credential = strings.TrimSpace(credentials[0])
	}
	credentialHash := ""
	if credential != "" {
		var err error
		credentialHash, err = hashPairingCredential(credential)
		if err != nil {
			return models.RemoteAccessInvite{}, err
		}
		// A staged client can safely retry after the first claim response was lost and
		// the invitation secret was already retired.
		if s.pairings != nil {
			pairing, err := s.pairings.GetByPeerID(ctx, peerID)
			if err != nil {
				return models.RemoteAccessInvite{}, err
			}
			if pairing != nil && pairing.RevokedAt == nil && pairing.CredentialHash != "" &&
				subtle.ConstantTimeCompare([]byte(pairing.CredentialHash), []byte(credentialHash)) == 1 {
				return s.latestClaimedInviteForPeer(ctx, peerID)
			}
		}
	}
	if s.pairings != nil && credentialHash == "" {
		return models.RemoteAccessInvite{}, ErrInvalidPairingCredential
	}
	tokenHash := HashInviteToken(token)
	now := s.now()
	var inv *models.RemoteAccessInvite
	var err error
	claimedAtomically := false
	if claimer, ok := s.pairings.(datastore.RemoteAccessPairingClaimer); ok {
		inv, err = claimer.ClaimInvite(ctx, tokenHash, peerID, credentialHash, uuid.NewString(), now)
		claimedAtomically = true
	} else {
		inv, err = s.invites.ClaimByTokenHash(ctx, tokenHash, peerID, now)
	}
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if inv == nil {
		existing, err := s.invites.GetByTokenHash(ctx, tokenHash)
		if err != nil {
			return models.RemoteAccessInvite{}, err
		}
		if existing == nil {
			return models.RemoteAccessInvite{}, ErrInviteNotFound
		}
		if existing.RevokedAt != nil {
			return models.RemoteAccessInvite{}, ErrInviteRevoked
		}
		if !now.Before(existing.ExpiresAt) {
			return models.RemoteAccessInvite{}, ErrInviteExpired
		}
		return models.RemoteAccessInvite{}, ErrInviteUsed
	}
	if s.pairings != nil && !claimedAtomically {
		pairing, err := s.pairings.GetByPeerID(ctx, peerID)
		if err != nil {
			return models.RemoteAccessInvite{}, err
		}
		if pairing == nil {
			pairing = &models.RemoteAccessPairing{
				ID: uuid.NewString(), PeerID: peerID, CreatedAt: now,
			}
		}
		inviteID := inv.ID
		pairing.InviteID = &inviteID
		pairing.CredentialHash = credentialHash
		pairing.PeerName = inv.PeerName
		pairing.CreatedBy = inv.CreatedBy
		pairing.RevokedAt = nil
		if pairing.CreatedAt.IsZero() {
			pairing.CreatedAt = now
		}
		if existing, err := s.pairings.Get(ctx, pairing.ID); err != nil {
			return models.RemoteAccessInvite{}, err
		} else if existing == nil {
			if err := s.pairings.Create(ctx, pairing); err != nil {
				return models.RemoteAccessInvite{}, err
			}
		} else if err := s.pairings.Update(ctx, pairing); err != nil {
			return models.RemoteAccessInvite{}, err
		}

		// The pairing is now the durable authorization. Retire both copies of the
		// invitation secret so backups and administrative APIs cannot disclose it.
		inv.ConnectionCode = ""
		inv.TokenHash = "claimed:" + inv.ID
		if err := s.invites.Update(ctx, inv); err != nil {
			return models.RemoteAccessInvite{}, err
		}
	}
	// The invite is now claimed, so its code no longer needs to be resolvable on the DHT;
	// drop it immediately rather than waiting for the next Supervise tick. Best-effort:
	// Supervise re-syncs on its timer, so a transient failure here self-heals.
	s.trySyncRendezvousCodes(ctx)
	inv.Token = ""
	return *inv, nil
}

func (s *Service) latestClaimedInviteForPeer(ctx context.Context, peerID string) (models.RemoteAccessInvite, error) {
	invites, err := s.invites.List(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	var matched *models.RemoteAccessInvite
	for i := range invites {
		inv := &invites[i]
		if inv.UsedAt == nil || inv.UsedByPeerID != peerID {
			continue
		}
		if matched == nil || inv.UsedAt.After(*matched.UsedAt) {
			matched = inv
		}
	}
	if matched == nil {
		return models.RemoteAccessInvite{}, ErrInviteNotFound
	}
	return *matched, nil
}

func (s *Service) UpgradePairingCredential(ctx context.Context, peerID, credential string) error {
	if s.pairings == nil {
		return errors.New("remote access pairings are not configured")
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ErrInvalidPeerID
	}
	hash, err := hashPairingCredential(credential)
	if err != nil {
		return err
	}
	pairing, err := s.pairings.GetByPeerID(ctx, peerID)
	if err != nil {
		return err
	}
	if pairing == nil {
		return ErrInviteNotFound
	}
	if pairing.RevokedAt != nil {
		return ErrInviteRevoked
	}
	if pairing.CredentialHash != "" && subtle.ConstantTimeCompare([]byte(pairing.CredentialHash), []byte(hash)) != 1 {
		return ErrInvalidPairingCredential
	}
	pairing.CredentialHash = hash
	return s.pairings.Update(ctx, pairing)
}

func (s *Service) ResolveInvite(ctx context.Context, token string) (models.RemoteAccessInvite, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return models.RemoteAccessInvite{}, ErrInvalidToken
	}
	inv, err := s.invites.GetByTokenHash(ctx, HashInviteToken(token))
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if inv == nil {
		return models.RemoteAccessInvite{}, ErrInviteNotFound
	}
	now := s.now()
	if inv.RevokedAt != nil {
		return models.RemoteAccessInvite{}, ErrInviteRevoked
	}
	if inv.UsedAt != nil {
		return models.RemoteAccessInvite{}, ErrInviteUsed
	}
	if inv.UsedAt == nil && !now.Before(inv.ExpiresAt) {
		return models.RemoteAccessInvite{}, ErrInviteExpired
	}
	if s.host == nil {
		return models.RemoteAccessInvite{}, errors.New("iroh host manager not configured")
	}
	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if inv.IrohInvite != irohInvite {
		inv.IrohInvite = irohInvite
		if err := s.invites.Update(ctx, inv); err != nil {
			return models.RemoteAccessInvite{}, err
		}
	}
	inv.Token = ""
	return *inv, nil
}

func (s *Service) ResolveClaimedInviteForPeer(ctx context.Context, peerID string) (models.RemoteAccessInvite, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return models.RemoteAccessInvite{}, ErrInvalidToken
	}
	invites, err := s.invites.List(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	var matched *models.RemoteAccessInvite
	for i := range invites {
		inv := &invites[i]
		if inv.RevokedAt != nil || inv.UsedAt == nil || inv.UsedByPeerID != peerID {
			continue
		}
		if matched == nil || inv.UsedAt.After(*matched.UsedAt) {
			matched = inv
		}
	}
	if matched == nil {
		return models.RemoteAccessInvite{}, ErrInviteNotFound
	}
	if s.host == nil {
		return models.RemoteAccessInvite{}, errors.New("iroh host manager not configured")
	}
	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if matched.IrohInvite != irohInvite {
		matched.IrohInvite = irohInvite
		if err := s.invites.Update(ctx, matched); err != nil {
			return models.RemoteAccessInvite{}, err
		}
	}
	matched.Token = ""
	return *matched, nil
}

func (s *Service) Supervise(ctx context.Context) (SyncSummary, error) {
	invites, err := s.invites.List(ctx)
	if err != nil {
		return SyncSummary{}, err
	}
	active := filterActiveInvites(invites, s.now())
	summary := SyncSummary{Active: len(active)}
	// Keep the host's rendezvous file in sync with the active set on every supervise
	// pass (startup + the 1-minute ticker + after revoke), covering both newly added
	// codes and emptying the file once the last invite lapses.
	s.trySyncRendezvousCodes(ctx)
	activePairings := 0
	if s.pairings != nil {
		pairings, err := s.pairings.List(ctx)
		if err != nil {
			return summary, err
		}
		for i := range pairings {
			if pairings[i].IsActive() {
				activePairings++
			}
		}
	}
	if len(active) == 0 && activePairings == 0 {
		if s.host != nil {
			if status := s.host.Status(ctx); status.Running {
				if err := s.host.Stop(ctx); err != nil {
					return summary, err
				}
				summary.Stopped = true
			}
		}
		return summary, nil
	}
	if s.host == nil {
		return summary, errors.New("iroh host manager not configured")
	}
	wasRunning := s.host.Status(ctx).Running
	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return summary, err
	}
	summary.Started = !wasRunning
	for i := range active {
		if active[i].IrohInvite == irohInvite {
			continue
		}
		active[i].IrohInvite = irohInvite
		if err := s.invites.Update(ctx, &active[i]); err != nil {
			return summary, err
		}
		summary.Updated++
	}
	publishable := filterPublishableInvites(active, s.now())
	codes := make([]string, 0, len(publishable))
	for i := range publishable {
		if code := strings.TrimSpace(publishable[i].ConnectionCode); code != "" {
			codes = append(codes, code)
		}
	}
	s.tryPublishRendezvousCodes(ctx, codes, irohInvite)
	return summary, nil
}

// trySyncRendezvousCodes runs syncRendezvousCodes and logs (but does not propagate) any
// error, for call sites where the rendezvous file is a side effect rather than the result.
func (s *Service) trySyncRendezvousCodes(ctx context.Context) {
	if err := s.syncRendezvousCodes(ctx); err != nil {
		log.Printf("[remote-access] failed to sync rendezvous codes: %v", err)
	}
}

func (s *Service) tryPublishRendezvousCodes(ctx context.Context, codes []string, invite string) {
	publisher, ok := s.host.(RendezvousImmediatePublisher)
	if !ok || len(codes) == 0 || strings.TrimSpace(invite) == "" {
		return
	}
	if err := publisher.PublishRendezvousRecords(ctx, codes, invite); err != nil {
		log.Printf("[remote-access] failed to publish rendezvous codes: %v", err)
	}
}

// syncRendezvousCodes mirrors the active connection codes into the host's rendezvous
// file (if the host supports DHT publishing). Best-effort: failures are non-fatal to the
// invite operation that triggered the sync, since the host re-reads the file on a timer.
func (s *Service) syncRendezvousCodes(ctx context.Context) error {
	publisher, ok := s.host.(RendezvousPublisher)
	if !ok {
		return nil
	}
	path := strings.TrimSpace(publisher.RendezvousFilePath())
	if path == "" {
		return nil
	}
	invites, err := s.invites.List(ctx)
	if err != nil {
		return err
	}
	// Only publish codes for invites still awaiting their first claim. Once an invite is
	// claimed, the paired client reconnects via the host's stable iroh NodeID rather than
	// re-resolving the code. Removing it here stops future republishing; public DHT nodes
	// may retain the last item until clients reject its signed timestamp at the rendezvous
	// TTL.
	publishable := filterPublishableInvites(invites, s.now())

	var b strings.Builder
	b.WriteString("# strmr pending connection codes — managed by remoteaccess.Service; do not edit\n")
	for i := range publishable {
		code := strings.TrimSpace(publishable[i].ConnectionCode)
		if code != "" {
			b.WriteString(code)
			b.WriteByte('\n')
		}
	}

	// Write atomically so the host never reads a half-written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// RendezvousFilePath returns the path the host watches for active codes, or "" if the
// configured host does not support DHT rendezvous publishing.
func (s *Service) RendezvousFilePath() string {
	if publisher, ok := s.host.(RendezvousPublisher); ok {
		if path := strings.TrimSpace(publisher.RendezvousFilePath()); path != "" {
			return filepath.Clean(path)
		}
	}
	return ""
}

func HashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func hashPairingCredential(credential string) (string, error) {
	credential = strings.TrimSpace(credential)
	if len(credential) < 32 || len(credential) > 256 {
		return "", ErrInvalidPairingCredential
	}
	for _, char := range credential {
		if char < 0x21 || char > 0x7e {
			return "", ErrInvalidPairingCredential
		}
	}
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:]), nil
}

// codeAlphabet is Crockford base32 (no I, L, O, U) so codes are unambiguous to read
// aloud or type. Each character carries 5 bits of entropy.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// codeBodyLength is the number of random base32 characters in a connection code.
// 18 chars * 5 bits = ~90 bits of entropy, formatted as three groups of six. This is
// the entire security boundary of the rendezvous record (the DHT signing key is derived
// from the code), so it must be high enough to resist an offline brute-force of the
// published, code-derived public key. See backend/iroh-host/src/rendezvous.rs.
const codeBodyLength = 18

func generateToken() (string, error) {
	buf := make([]byte, codeBodyLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate remote access invite token: %w", err)
	}
	// Masking a uniform byte to its low 5 bits is unbiased because 256 is an exact
	// multiple of 32, so every alphabet index is equally likely.
	body := make([]byte, codeBodyLength)
	for i, b := range buf {
		body[i] = codeAlphabet[b&0x1f]
	}
	return "mshost-" + string(body[0:6]) + "-" + string(body[6:12]) + "-" + string(body[12:18]), nil
}

func countActiveInvites(invites []models.RemoteAccessInvite, now time.Time) int {
	return len(filterActiveInvites(invites, now))
}

func filterActiveInvites(invites []models.RemoteAccessInvite, now time.Time) []models.RemoteAccessInvite {
	active := make([]models.RemoteAccessInvite, 0, len(invites))
	for _, inv := range invites {
		if inv.IsActive(now) {
			active = append(active, inv)
		}
	}
	return active
}

// filterPublishableInvites returns the invites whose connection codes should be live on
// the rendezvous DHT — i.e. those still awaiting their first claim. Claimed invites stay
// "active" (the host keeps running for them) but are no longer published.
func filterPublishableInvites(invites []models.RemoteAccessInvite, now time.Time) []models.RemoteAccessInvite {
	pending := make([]models.RemoteAccessInvite, 0, len(invites))
	for _, inv := range invites {
		if inv.IsPendingClaim(now) {
			pending = append(pending, inv)
		}
	}
	return pending
}
