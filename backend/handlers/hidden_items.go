package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"novastream/models"
	"novastream/services/hiddenitems"

	"github.com/gorilla/mux"
)

type hiddenItemsService interface {
	List(userID string) ([]models.HiddenItem, error)
	Hide(userID string, input models.HiddenItemUpsert) (models.HiddenItem, error)
	Unhide(userID, mediaType, id string) (bool, error)
	IsHidden(userID, mediaType, id string, externalIDs map[string]string) bool
	FilterHiddenWatchlistItems(userID string, items []models.WatchlistItem) []models.WatchlistItem
	ShouldHideTitleMap(userID string, item map[string]interface{}) bool
}

var _ hiddenItemsService = (*hiddenitems.Service)(nil)

type HiddenItemsHandler struct {
	Service hiddenItemsService
	Users   userService
}

func NewHiddenItemsHandler(service hiddenItemsService, users userService) *HiddenItemsHandler {
	return &HiddenItemsHandler{Service: service, Users: users}
}

func (h *HiddenItemsHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	items, err := h.Service.List(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *HiddenItemsHandler) Hide(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body models.HiddenItemUpsert
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	item, err := h.Service.Hide(userID, body)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, hiddenitems.ErrUserIDRequired),
			errors.Is(err, hiddenitems.ErrIDRequired),
			errors.Is(err, hiddenitems.ErrMediaTypeRequired):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(item)
}

func (h *HiddenItemsHandler) Unhide(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	vars := mux.Vars(r)
	removed, err := h.Service.Unhide(userID, vars["mediaType"], vars["id"])
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, hiddenitems.ErrUserIDRequired) || errors.Is(err, hiddenitems.ErrIdentifierRequired) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !removed {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HiddenItemsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *HiddenItemsHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := strings.TrimSpace(mux.Vars(r)["userID"])
	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return "", false
	}
	if h.Users != nil && !h.Users.Exists(userID) {
		http.Error(w, "user not found", http.StatusNotFound)
		return "", false
	}
	return userID, true
}
