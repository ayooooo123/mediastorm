package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/remoteaccess"
)

type RemoteAccessHandler struct {
	service *remoteaccess.Service
}

func NewRemoteAccessHandler(service *remoteaccess.Service) *RemoteAccessHandler {
	return &RemoteAccessHandler{service: service}
}

// RemoteAccessRevocationMiddleware gates requests arriving through the trusted
// Iroh host proxy to devices with a consumed, non-revoked pairing. Health and
// the one-time claim operation must remain reachable before pairing completes.
func RemoteAccessRevocationMiddleware(service *remoteaccess.Service) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Mediastorm-Iroh-Proxy") != "1" {
				next.ServeHTTP(w, r)
				return
			}
			if remoteAccessPrePairingPathAllowed(r) {
				next.ServeHTTP(w, r)
				return
			}
			authorized, err := service.AuthorizePeer(
				r.Context(),
				r.Header.Get("X-Client-ID"),
				r.Header.Get("X-Remote-Access-Credential"),
			)
			if err != nil {
				log.Printf("remote access pairing check failed: %v", err)
				writeJSONError(w, "remote access pairing check unavailable", http.StatusServiceUnavailable)
				return
			}
			if !authorized {
				writeJSONError(w, "device is not paired or access has been revoked", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func remoteAccessPrePairingPathAllowed(r *http.Request) bool {
	if r.Method == http.MethodOptions || r.URL.Path == "/health" {
		return true
	}
	return r.Method == http.MethodPost && r.URL.Path == "/api/remote-access/invites/claim"
}

type createRemoteAccessInviteRequest struct {
	PeerName       string `json:"peerName"`
	ExpiresInHours int    `json:"expiresInHours"`
}

type claimRemoteAccessInviteRequest struct {
	Token      string `json:"token"`
	PeerID     string `json:"peerId"`
	Credential string `json:"credential"`
}

type upgradeRemoteAccessPairingRequest struct {
	PeerID     string `json:"peerId"`
	Credential string `json:"credential"`
}

type resolveRemoteAccessInviteRequest struct {
	Token string `json:"token"`
}

type remoteAccessInviteResponse struct {
	ID             string     `json:"id"`
	Token          string     `json:"token,omitempty"`
	ConnectionCode string     `json:"connectionCode,omitempty"`
	IrohInvite     string     `json:"irohInvite,omitempty"`
	PeerName       string     `json:"peerName,omitempty"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	UsedAt         *time.Time `json:"usedAt,omitempty"`
	UsedByPeerID   string     `json:"usedByPeerId,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func (h *RemoteAccessHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, h.service.Status(r.Context()))
}

func (h *RemoteAccessHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	var req createRemoteAccessInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ExpiresInHours < 0 {
		writeJSONError(w, "expiresInHours must be zero or greater", http.StatusBadRequest)
		return
	}
	inv, err := h.service.CreateInvite(r.Context(), auth.GetAccountID(r), remoteaccess.CreateInviteRequest{
		PeerName:  req.PeerName,
		ExpiresIn: time.Duration(req.ExpiresInHours) * time.Hour,
	})
	if err != nil {
		log.Printf("[remote-access] create invite failed peerName=%q: %v", req.PeerName, err)
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	h.writeJSON(w, toRemoteAccessInviteResponse(inv))
}

func (h *RemoteAccessHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := h.service.ListInvites(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result := make([]remoteAccessInviteResponse, 0, len(invites))
	for _, inv := range invites {
		result = append(result, toRemoteAccessInviteResponse(inv))
	}
	h.writeJSON(w, result)
}

func (h *RemoteAccessHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["inviteID"])
	if err := h.service.RevokeInvite(r.Context(), id); err != nil {
		if errors.Is(err, remoteaccess.ErrInviteNotFound) {
			writeJSONError(w, "remote access invite not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RemoteAccessHandler) ResolveInvite(w http.ResponseWriter, r *http.Request) {
	var req resolveRemoteAccessInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	inv, err := h.service.ResolveInvite(r.Context(), req.Token)
	if err != nil {
		status := remoteAccessErrorStatus(err)
		writeJSONError(w, err.Error(), status)
		return
	}
	h.writeJSON(w, map[string]any{
		"id":             inv.ID,
		"connectionCode": inv.ConnectionCode,
		"irohInvite":     inv.IrohInvite,
		"expiresAt":      inv.ExpiresAt,
	})
}

func (h *RemoteAccessHandler) ResolveClaimedInvite(w http.ResponseWriter, r *http.Request) {
	peerID := strings.TrimSpace(r.URL.Query().Get("peerId"))
	if peerID == "" {
		peerID = strings.TrimSpace(r.Header.Get("X-Client-ID"))
	}
	inv, err := h.service.ResolveClaimedInviteForPeer(r.Context(), peerID)
	if err != nil {
		writeJSONError(w, err.Error(), remoteAccessErrorStatus(err))
		return
	}
	h.writeJSON(w, map[string]any{
		"id":           inv.ID,
		"hostInvite":   inv.IrohInvite,
		"usedAt":       inv.UsedAt,
		"usedByPeerId": inv.UsedByPeerID,
	})
}

func (h *RemoteAccessHandler) ClaimInvite(w http.ResponseWriter, r *http.Request) {
	var req claimRemoteAccessInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	peerID := strings.TrimSpace(req.PeerID)
	if headerPeerID := strings.TrimSpace(r.Header.Get("X-Client-ID")); headerPeerID == "" || headerPeerID != peerID {
		writeJSONError(w, remoteaccess.ErrInvalidPeerID.Error(), http.StatusBadRequest)
		return
	}
	credential := strings.TrimSpace(req.Credential)
	if headerCredential := strings.TrimSpace(r.Header.Get("X-Remote-Access-Credential")); headerCredential == "" || headerCredential != credential {
		writeJSONError(w, remoteaccess.ErrInvalidPairingCredential.Error(), http.StatusBadRequest)
		return
	}
	inv, err := h.service.ClaimInvite(r.Context(), req.Token, peerID, credential)
	if err != nil {
		writeJSONError(w, err.Error(), remoteAccessErrorStatus(err))
		return
	}
	h.writeJSON(w, map[string]any{
		"id":           inv.ID,
		"peerName":     inv.PeerName,
		"usedAt":       inv.UsedAt,
		"usedByPeerId": inv.UsedByPeerID,
	})
}

func (h *RemoteAccessHandler) UpgradePairingCredential(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Mediastorm-Iroh-Proxy") != "1" {
		writeJSONError(w, "pairing credential upgrades must use the paired Iroh transport", http.StatusForbidden)
		return
	}
	var req upgradeRemoteAccessPairingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	peerID := strings.TrimSpace(req.PeerID)
	if headerPeerID := strings.TrimSpace(r.Header.Get("X-Client-ID")); headerPeerID == "" || headerPeerID != peerID {
		writeJSONError(w, remoteaccess.ErrInvalidPeerID.Error(), http.StatusBadRequest)
		return
	}
	credential := strings.TrimSpace(req.Credential)
	if headerCredential := strings.TrimSpace(r.Header.Get("X-Remote-Access-Credential")); headerCredential == "" || headerCredential != credential {
		writeJSONError(w, remoteaccess.ErrInvalidPairingCredential.Error(), http.StatusBadRequest)
		return
	}
	if err := h.service.UpgradePairingCredential(r.Context(), peerID, credential); err != nil {
		writeJSONError(w, err.Error(), remoteAccessErrorStatus(err))
		return
	}
	h.writeJSON(w, map[string]any{"ok": true})
}

func (h *RemoteAccessHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-PIN, X-Client-ID, X-Remote-Access-Credential")
	w.Header().Set("Access-Control-Max-Age", strconv.Itoa(86400))
	w.WriteHeader(http.StatusOK)
}

func (h *RemoteAccessHandler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func toRemoteAccessInviteResponse(inv models.RemoteAccessInvite) remoteAccessInviteResponse {
	return remoteAccessInviteResponse{
		ID:             inv.ID,
		Token:          inv.Token,
		ConnectionCode: inv.ConnectionCode,
		IrohInvite:     inv.IrohInvite,
		PeerName:       inv.PeerName,
		ExpiresAt:      inv.ExpiresAt,
		UsedAt:         inv.UsedAt,
		UsedByPeerID:   inv.UsedByPeerID,
		RevokedAt:      inv.RevokedAt,
		CreatedAt:      inv.CreatedAt,
	}
}

func remoteAccessErrorStatus(err error) int {
	switch {
	case errors.Is(err, remoteaccess.ErrInviteNotFound):
		return http.StatusNotFound
	case errors.Is(err, remoteaccess.ErrInviteExpired), errors.Is(err, remoteaccess.ErrInviteUsed), errors.Is(err, remoteaccess.ErrInviteRevoked), errors.Is(err, remoteaccess.ErrInvalidToken), errors.Is(err, remoteaccess.ErrInvalidPeerID), errors.Is(err, remoteaccess.ErrInvalidPairingCredential):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
