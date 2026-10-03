package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"novastream/models"
)

// RegisterSportsUI uses the browser's existing cookie authentication. Profile
// ownership is checked separately from permission to change server availability.
func (h *AdminUIHandler) RegisterSportsUI(r *mux.Router, sports *SportsHandler) {
	h.sportsHandler = sports
	for _, prefix := range []string{"/admin", "/account"} {
		r.HandleFunc(prefix+"/sports", h.RequireAuth(h.SportsPage)).Methods(http.MethodGet)
		r.HandleFunc(prefix+"/api/sports/catalog", h.RequireAuth(h.SportsCatalogAPI)).Methods(http.MethodGet)
		r.HandleFunc(prefix+"/api/sports/settings", h.RequireAuth(h.SportsSettingsAPI)).Methods(http.MethodGet, http.MethodPut)
		r.HandleFunc(prefix+"/api/sports/profiles/{userID}", h.RequireAuth(h.SportsProfileAPI)).Methods(http.MethodGet, http.MethodPut)
	}
}

func (h *AdminUIHandler) SportsPage(w http.ResponseWriter, r *http.Request) {
	master, account, base, username := h.getPageRoleInfo(r)
	data := AdminPageData{CurrentPath: base + "/sports", BasePath: base, ServerBasePath: h.serverBasePath,
		IsAdmin: master, AccountID: account, Username: username, Users: h.getScopedUsers(master, account),
		Version: GetBackendVersion(), BuildID: GetBackendBuildID()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.sportsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, "Unable to render Sports Hub settings", http.StatusInternalServerError)
	}
}

func (h *AdminUIHandler) SportsSettingsAPI(w http.ResponseWriter, r *http.Request) {
	master, _, _, _ := h.getPageRoleInfo(r)
	if !master {
		writeSportsPreferenceError(w, "Only the server administrator can change league availability", http.StatusForbidden)
		return
	}
	if h.sportsHandler == nil {
		writeSportsPreferenceError(w, "Sports settings unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPut {
		h.sportsHandler.PutSettings(w, r)
	} else {
		h.sportsHandler.GetSettings(w, r)
	}
}

func (h *AdminUIHandler) SportsProfileAPI(w http.ResponseWriter, r *http.Request) {
	master, account, _, _ := h.getPageRoleInfo(r)
	id := mux.Vars(r)["userID"]
	allowed := false
	for _, profile := range h.getScopedUsers(master, account) {
		if profile.ID == id {
			allowed = true
			break
		}
	}
	if !allowed {
		writeSportsPreferenceError(w, "Profile not found", http.StatusNotFound)
		return
	}
	if h.userSettingsService == nil {
		writeSportsPreferenceError(w, "Sports preferences unavailable", http.StatusServiceUnavailable)
		return
	}
	handler := NewUserSettingsHandler(h.userSettingsService, h.usersService, h.configManager)
	if r.Method == http.MethodPut {
		handler.PutSportsPreferences(w, r)
	} else {
		handler.GetSportsPreferences(w, r)
	}
}

func (h *AdminUIHandler) SportsCatalogAPI(w http.ResponseWriter, r *http.Request) {
	if h.sportsHandler == nil {
		writeSportsPreferenceError(w, "Sports catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	service := h.sportsHandler.service
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("teams") != "1" {
		writeSportsJSON(w, map[string]any{"leagues": service.Leagues(), "enabled": service.GetStatus().Enabled})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var teams []models.SportsTeamRecord
	var err error
	if id := r.URL.Query().Get("league"); id != "" {
		if _, ok := service.League(id); !ok {
			writeSportsPreferenceError(w, "League not found", http.StatusNotFound)
			return
		}
		teams, err = service.EnsureLeagueTeamCatalog(ctx, id)
	} else {
		teams, err = service.EnsureTeamCatalog(ctx)
	}
	if teams == nil {
		teams = []models.SportsTeamRecord{}
	}
	// Keep successful league catalogs usable when one provider request fails.
	writeSportsJSON(w, map[string]any{"teams": teams, "partial": err != nil})
}
