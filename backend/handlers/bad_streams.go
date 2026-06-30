package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"novastream/services/badstreams"
)

type BadStreamsHandler struct {
	service *badstreams.Service
}

type badStreamsListResponse struct {
	Streams    []badstreams.Entry `json:"streams"`
	Items      []badstreams.Entry `json:"items"`
	Total      int                `json:"total"`
	Page       int                `json:"page"`
	PerPage    int                `json:"perPage"`
	TotalPages int                `json:"totalPages"`
}

func NewBadStreamsHandler(service *badstreams.Service) *BadStreamsHandler {
	return &BadStreamsHandler{service: service}
}

func (h *BadStreamsHandler) List(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	streams := filterBadStreams(h.service.List(), r.URL.Query().Get("q"))
	page, perPage := parseBadStreamsPage(r)
	total := len(streams)
	totalPages := 0
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	if page > totalPages && totalPages > 0 {
		page = totalPages
	}
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	items := streams[start:end]
	writeBadStreamsJSON(w, badStreamsListResponse{
		Streams:    items,
		Items:      items,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: totalPages,
	})
}

func (h *BadStreamsHandler) Mark(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req badstreams.MarkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entry, err := h.service.Mark(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeBadStreamsJSON(w, entry)
}

func (h *BadStreamsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	id := mux.Vars(r)["id"]
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	deleted := h.service.Delete(id)
	writeBadStreamsJSON(w, map[string]interface{}{"deleted": deleted})
}

func (h *BadStreamsHandler) Clear(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "bad stream service unavailable", http.StatusServiceUnavailable)
		return
	}
	count := h.service.Clear()
	writeBadStreamsJSON(w, map[string]interface{}{"deleted": count})
}

func (h *BadStreamsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func writeBadStreamsJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func parseBadStreamsPage(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
	if perPage < 1 {
		perPage = 25
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func filterBadStreams(streams []badstreams.Entry, query string) []badstreams.Entry {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return streams
	}
	filtered := make([]badstreams.Entry, 0, len(streams))
	for _, stream := range streams {
		if badStreamMatchesQuery(stream, query) {
			filtered = append(filtered, stream)
		}
	}
	return filtered
}

func badStreamMatchesQuery(stream badstreams.Entry, query string) bool {
	fields := []string{
		stream.ReleaseName,
		stream.NormalizedReleaseName,
		stream.ServiceType,
		stream.Provider,
		stream.SourcePath,
		stream.Reason,
		stream.ID,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}
