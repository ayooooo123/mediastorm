package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/remoteaccess"
)

type middlewareInviteRepo struct {
	invites []models.RemoteAccessInvite
	listErr error
}

type middlewarePairingRepo struct {
	pairing *models.RemoteAccessPairing
}

func (r *middlewarePairingRepo) Get(_ context.Context, id string) (*models.RemoteAccessPairing, error) {
	if r.pairing == nil || r.pairing.ID != id {
		return nil, nil
	}
	copy := *r.pairing
	return &copy, nil
}
func (r *middlewarePairingRepo) GetByPeerID(_ context.Context, peerID string) (*models.RemoteAccessPairing, error) {
	if r.pairing == nil || r.pairing.PeerID != peerID {
		return nil, nil
	}
	copy := *r.pairing
	return &copy, nil
}
func (r *middlewarePairingRepo) List(context.Context) ([]models.RemoteAccessPairing, error) {
	if r.pairing == nil {
		return nil, nil
	}
	return []models.RemoteAccessPairing{*r.pairing}, nil
}
func (r *middlewarePairingRepo) Create(_ context.Context, pairing *models.RemoteAccessPairing) error {
	copy := *pairing
	r.pairing = &copy
	return nil
}
func (r *middlewarePairingRepo) Update(ctx context.Context, pairing *models.RemoteAccessPairing) error {
	return r.Create(ctx, pairing)
}
func (r *middlewarePairingRepo) Delete(context.Context, string) error { r.pairing = nil; return nil }
func (r *middlewarePairingRepo) Count(context.Context) (int64, error) {
	if r.pairing == nil {
		return 0, nil
	}
	return 1, nil
}

func TestClaimInviteRequiresBodyAndHeaderDeviceIdentityToMatch(t *testing.T) {
	handler := NewRemoteAccessHandler(remoteaccess.NewService(&middlewareInviteRepo{}, nil))
	for _, tc := range []struct {
		name     string
		headerID string
	}{
		{name: "missing header"},
		{name: "different header", headerID: "device-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/remote-access/invites/claim",
				strings.NewReader(`{"token":"mshost-ABCDEF-GHJKMN-PQRSTV","peerId":"device-1"}`),
			)
			req.Header.Set("Content-Type", "application/json")
			if tc.headerID != "" {
				req.Header.Set("X-Client-ID", tc.headerID)
			}
			response := httptest.NewRecorder()

			handler.ClaimInvite(response, req)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), remoteaccess.ErrInvalidPeerID.Error()) {
				t.Fatalf("body = %q, want invalid peer id", response.Body.String())
			}
		})
	}
}

func (r *middlewareInviteRepo) Get(context.Context, string) (*models.RemoteAccessInvite, error) {
	return nil, nil
}
func (r *middlewareInviteRepo) GetByTokenHash(context.Context, string) (*models.RemoteAccessInvite, error) {
	return nil, nil
}
func (r *middlewareInviteRepo) List(context.Context) ([]models.RemoteAccessInvite, error) {
	return r.invites, r.listErr
}
func (r *middlewareInviteRepo) Create(context.Context, *models.RemoteAccessInvite) error {
	return nil
}
func (r *middlewareInviteRepo) ClaimByTokenHash(context.Context, string, string, time.Time) (*models.RemoteAccessInvite, error) {
	return nil, nil
}
func (r *middlewareInviteRepo) Update(context.Context, *models.RemoteAccessInvite) error {
	return nil
}
func (r *middlewareInviteRepo) Delete(context.Context, string) error { return nil }
func (r *middlewareInviteRepo) Count(context.Context) (int64, error) { return 0, nil }

func TestRemoteAccessRevocationMiddlewareGatesIrohRequestsToPairedDevices(t *testing.T) {
	now := time.Now().UTC()
	revokedAt := now.Add(time.Minute)
	repo := &middlewareInviteRepo{invites: []models.RemoteAccessInvite{
		{UsedAt: &now, UsedByPeerID: "active-device"},
		{UsedAt: &now, UsedByPeerID: "revoked-device", RevokedAt: &revokedAt},
	}}
	middleware := RemoteAccessRevocationMiddleware(remoteaccess.NewService(repo, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := middleware(next)

	tests := []struct {
		name     string
		method   string
		path     string
		proxied  bool
		clientID string
		want     int
	}{
		{name: "direct request bypasses pairing gate", method: http.MethodGet, path: "/api/settings", want: http.StatusNoContent},
		{name: "health is available before pairing", method: http.MethodGet, path: "/health", proxied: true, want: http.StatusNoContent},
		{name: "api-prefixed health is available before pairing", method: http.MethodGet, path: "/api/health", proxied: true, want: http.StatusNoContent},
		{name: "health with trailing slash is available before pairing", method: http.MethodGet, path: "/health/", proxied: true, want: http.StatusNoContent},
		{name: "claim is available before pairing", method: http.MethodPost, path: "/api/remote-access/invites/claim", proxied: true, want: http.StatusNoContent},
		{name: "similar path is not a pre-pairing bypass", method: http.MethodPost, path: "/other/remote-access/invites/claim", proxied: true, want: http.StatusForbidden},
		{name: "active pairing passes", method: http.MethodGet, path: "/api/settings", proxied: true, clientID: "active-device", want: http.StatusNoContent},
		{name: "missing device is rejected", method: http.MethodGet, path: "/api/settings", proxied: true, want: http.StatusForbidden},
		{name: "unknown device is rejected", method: http.MethodGet, path: "/api/settings", proxied: true, clientID: "unknown-device", want: http.StatusForbidden},
		{name: "revoked device is rejected", method: http.MethodGet, path: "/api/settings", proxied: true, clientID: "revoked-device", want: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.proxied {
				req.Header.Set("X-Mediastorm-Iroh-Proxy", "1")
			}
			if tc.clientID != "" {
				req.Header.Set("X-Client-ID", tc.clientID)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tc.want, response.Body.String())
			}
		})
	}
}

func TestRemoteAccessRevocationMiddlewareFailsClosedWhenPairingLookupFails(t *testing.T) {
	repo := &middlewareInviteRepo{listErr: errors.New("database unavailable")}
	handler := RemoteAccessRevocationMiddleware(remoteaccess.NewService(repo, nil))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("X-Mediastorm-Iroh-Proxy", "1")
	req.Header.Set("X-Client-ID", "active-device")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestRemoteAccessMiddlewareRequiresCredentialForUpgradedPairing(t *testing.T) {
	credential := strings.Repeat("a", 43)
	digest := sha256.Sum256([]byte(credential))
	hash := hex.EncodeToString(digest[:])
	pairings := &middlewarePairingRepo{pairing: &models.RemoteAccessPairing{
		ID: "pairing-1", PeerID: "device-1", CredentialHash: hash,
	}}
	handler := RemoteAccessRevocationMiddleware(remoteaccess.NewService(&middlewareInviteRepo{}, nil, pairings))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	for _, tc := range []struct {
		name       string
		credential string
		want       int
	}{
		{name: "missing", want: http.StatusForbidden},
		{name: "wrong", credential: strings.Repeat("b", 43), want: http.StatusForbidden},
		{name: "matching", credential: credential, want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
			req.Header.Set("X-Mediastorm-Iroh-Proxy", "1")
			req.Header.Set("X-Client-ID", "device-1")
			if tc.credential != "" {
				req.Header.Set("X-Remote-Access-Credential", tc.credential)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
			}
		})
	}
}

func TestUpgradePairingCredentialRequiresTrustedIrohTransport(t *testing.T) {
	pairings := &middlewarePairingRepo{pairing: &models.RemoteAccessPairing{
		ID: "legacy-1", PeerID: "device-1", CreatedBy: "account-1",
	}}
	handler := NewRemoteAccessHandler(remoteaccess.NewService(&middlewareInviteRepo{}, nil, pairings))
	credential := strings.Repeat("c", 43)
	body := `{"peerId":"device-1","credential":"` + credential + `"}`

	direct := httptest.NewRequest(http.MethodPost, "/api/remote-access/pairings/credential", strings.NewReader(body))
	direct.Header.Set("X-Client-ID", "device-1")
	directResponse := httptest.NewRecorder()
	handler.UpgradePairingCredential(directResponse, direct)
	if directResponse.Code != http.StatusForbidden {
		t.Fatalf("direct status=%d want=%d", directResponse.Code, http.StatusForbidden)
	}

	proxied := httptest.NewRequest(http.MethodPost, "/api/remote-access/pairings/credential", strings.NewReader(body))
	proxied.Header.Set("X-Mediastorm-Iroh-Proxy", "1")
	proxied.Header.Set("X-Client-ID", "device-1")
	proxied.Header.Set("X-Remote-Access-Credential", credential)
	proxiedResponse := httptest.NewRecorder()
	handler.UpgradePairingCredential(proxiedResponse, proxied)
	if proxiedResponse.Code != http.StatusOK {
		t.Fatalf("proxied status=%d want=%d body=%s", proxiedResponse.Code, http.StatusOK, proxiedResponse.Body.String())
	}
	if pairings.pairing == nil || pairings.pairing.CredentialHash == "" {
		t.Fatal("legacy pairing credential was not upgraded")
	}
}
