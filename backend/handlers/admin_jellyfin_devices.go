package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
)

// jellyfinDeviceView is one Jellyfin bridge sign-in. The token itself never
// leaves the server; ID is a hash of it.
type jellyfinDeviceView struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"accountId"`
	Username    string    `json:"username"`
	ProfileID   string    `json:"profileId"`
	ProfileName string    `json:"profileName"`
	Client      string    `json:"client"`
	IPAddress   string    `json:"ipAddress,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func jellyfinDeviceID(token string) string {
	sum := sha256.Sum256([]byte("jellyfin-device|" + token))
	return hex.EncodeToString(sum[:12])
}

// JellyfinDevicesPage serves the Jellyfin bridge sign-in list.
func (h *AdminUIHandler) JellyfinDevicesPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)
	settings, err := config.NewManager(h.settingsPath).Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}
	data := AdminPageData{
		CurrentPath: basePath + "/jellyfin-devices", BasePath: basePath,
		ServerBasePath: h.serverBasePath, IsAdmin: isAdmin, AccountID: accountID,
		Username: username, Settings: settings, Version: GetBackendVersion(), BuildID: GetBackendBuildID(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.jfDevicesTemplate == nil {
		http.Error(w, "Jellyfin devices template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.jfDevicesTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Jellyfin devices template error: %v\n", err)
	}
}

// jellyfinSessions lists bridge sessions the caller may manage: every
// account's for the master account, otherwise the caller's own.
func (h *AdminUIHandler) jellyfinSessions(r *http.Request) []models.Session {
	if h.sessionsService == nil || h.accountsService == nil {
		return nil
	}
	isAdmin, accountID, _, _ := h.getPageRoleInfo(r)
	accountIDs := []string{accountID}
	if isAdmin {
		accountIDs = accountIDs[:0]
		for _, account := range h.accountsService.List() {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	var out []models.Session
	for _, id := range accountIDs {
		if id == "" {
			continue
		}
		for _, session := range h.sessionsService.GetSessionsForAccount(id) {
			if session.Scope == models.SessionScopeJellyfin {
				out = append(out, session)
			}
		}
	}
	return out
}

// GetJellyfinDevices returns the bridge sign-ins, newest first.
func (h *AdminUIHandler) GetJellyfinDevices(w http.ResponseWriter, r *http.Request) {
	sessions := h.jellyfinSessions(r)
	result := make([]jellyfinDeviceView, 0, len(sessions))
	for _, session := range sessions {
		view := jellyfinDeviceView{
			ID:        jellyfinDeviceID(session.Token),
			AccountID: session.AccountID,
			ProfileID: session.ScopeResource,
			Client:    strings.TrimSpace(strings.TrimPrefix(session.UserAgent, "Jellyfin:")),
			IPAddress: session.IPAddress,
			CreatedAt: session.CreatedAt,
			ExpiresAt: session.ExpiresAt,
		}
		if account, ok := h.accountsService.Get(session.AccountID); ok {
			view.Username = account.Username
		}
		if h.usersService != nil {
			if profile, ok := h.usersService.Get(session.ScopeResource); ok {
				view.ProfileName = profile.Name
			}
		}
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// RevokeJellyfinDevice signs one Jellyfin bridge session out.
func (h *AdminUIHandler) RevokeJellyfinDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["deviceID"])
	for _, session := range h.jellyfinSessions(r) {
		if jellyfinDeviceID(session.Token) != id {
			continue
		}
		if err := h.sessionsService.Revoke(session.Token); err != nil {
			http.Error(w, `{"error":"failed to sign the device out"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	http.Error(w, `{"error":"device not found"}`, http.StatusNotFound)
}
