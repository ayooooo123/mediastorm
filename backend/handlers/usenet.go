package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"novastream/models"
	usenetsvc "novastream/services/usenet"
)

type usenetHealthService interface {
	CheckHealth(ctx context.Context, candidate models.NZBResult) (*models.NZBHealthCheck, error)
}

// UsenetHandler exposes endpoints for NNTP-backed NZB health checks.
type UsenetHandler struct {
	Service usenetHealthService
}

var _ usenetHealthService = (*usenetsvc.Service)(nil)

func NewUsenetHandler(s usenetHealthService) *UsenetHandler {
	return &UsenetHandler{Service: s}
}

// CheckHealth accepts an NZB indexer result and returns segment availability information from Usenet.
func (h *UsenetHandler) CheckHealth(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Result models.NZBResult `json:"result"`
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	res, err := h.Service.CheckHealth(r.Context(), request.Result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
