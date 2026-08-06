package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/client_settings"
	"novastream/services/clients"

	"github.com/gorilla/mux"
)

type clientsService interface {
	Register(id, userID, deviceType, os, appVersion, deviceName, nickname string) (models.Client, error)
	Get(id string) (*models.Client, error)
	List() []models.Client
	ListByUser(userID string) []models.Client
	Rename(id, name string) (models.Client, error)
	SetNickname(id, nickname string) (models.Client, error)
	SetFilterEnabled(id string, enabled bool) (models.Client, error)
	ReassignUser(id, newUserID string) (models.Client, error)
	UpdateLastSeen(id string) error
	Delete(id string) error
}

type clientSettingsService interface {
	Get(clientID string) (*models.ClientFilterSettings, error)
	Update(clientID string, settings models.ClientFilterSettings) error
	Delete(clientID string) error
}

type clientOwnershipService interface {
	BelongsToAccount(profileID, accountID string) bool
}

var _ clientsService = (*clients.Service)(nil)
var _ clientSettingsService = (*client_settings.Service)(nil)

// pendingPing stores the timestamp when a ping was requested for a client
type pendingPing struct {
	timestamp time.Time
}

type pendingClientMessage struct {
	ID                  string
	Message             string
	TargetAll           bool
	ProfileIDs          map[string]struct{}
	TargetProfileCount  int
	CreatedAt           time.Time
	DeliveredProfileIDs map[string]struct{}
}

type ClientsHandler struct {
	clients         clientsService
	settings        clientSettingsService
	users           clientOwnershipService
	pendingPings    map[string]pendingPing
	pendingMessages []pendingClientMessage
	pingMu          sync.RWMutex
	messageMu       sync.Mutex
}

const pingExpiry = 30 * time.Second // Pings expire after 30 seconds
const clientMessageExpiry = 24 * time.Hour
const clientMessageMaxLength = 1000

func NewClientsHandler(clientsSvc clientsService, settingsSvc clientSettingsService, usersSvc ...clientOwnershipService) *ClientsHandler {
	h := &ClientsHandler{
		clients:         clientsSvc,
		settings:        settingsSvc,
		pendingPings:    make(map[string]pendingPing),
		pendingMessages: make([]pendingClientMessage, 0),
	}
	if len(usersSvc) > 0 {
		h.users = usersSvc[0]
	}
	return h
}

func (h *ClientsHandler) canAccessProfile(r *http.Request, profileID string) bool {
	if auth.IsMaster(r) {
		return true
	}
	accountID := auth.GetAccountID(r)
	return accountID != "" && h.users != nil && h.users.BelongsToAccount(profileID, accountID)
}

func (h *ClientsHandler) getAuthorizedClient(w http.ResponseWriter, r *http.Request, clientID string) (*models.Client, bool) {
	client, err := h.clients.Get(clientID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if client == nil || !h.canAccessProfile(r, client.UserID) {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return nil, false
	}
	return client, true
}

// ClientRegistrationRequest is the request body for registering a client
type ClientRegistrationRequest struct {
	ID         string `json:"id"`
	UserID     string `json:"userId"`
	DeviceName string `json:"deviceName"`
	Nickname   string `json:"nickname"`
	DeviceType string `json:"deviceType"`
	OS         string `json:"os"`
	AppVersion string `json:"appVersion"`
}

// Register handles POST /api/clients/register
// Registers or updates a client device
func (h *ClientsHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req ClientRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.ID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	if !h.canAccessProfile(r, req.UserID) {
		writeJSONError(w, "user not found", http.StatusNotFound)
		return
	}
	// Intentionally do not block when the hardware client ID is already tied
	// to another account's profile (or an orphaned/deleted profile). Client
	// IDs are device-bound; an authenticated caller may reclaim the device
	// for a profile they own. The previous check returned 404 "client not
	// found" here and made nickname save fail forever for guest accounts and
	// reused hardware. Register overwrites UserID when a non-empty userId is
	// provided (already authorized above via canAccessProfile).
	client, err := h.clients.Register(req.ID, req.UserID, req.DeviceType, req.OS, req.AppVersion, req.DeviceName, req.Nickname)
	if err != nil {
		if errors.Is(err, clients.ErrUserNotFound) {
			writeJSONError(w, "user not found: "+req.UserID, http.StatusBadRequest)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"client": client,
	})
}

// ClientWithOverrides extends Client with hasOverrides flag for UI
type ClientWithOverrides struct {
	models.Client
	HasOverrides bool `json:"hasOverrides"`
}

// List handles GET /api/clients
// Returns all clients (master only) or clients for a specific user if userId query param is provided
func (h *ClientsHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")

	var clientList []models.Client
	if userID != "" {
		if !h.canAccessProfile(r, userID) {
			writeJSONError(w, "user not found", http.StatusNotFound)
			return
		}
		clientList = h.clients.ListByUser(userID)
	} else {
		for _, client := range h.clients.List() {
			if h.canAccessProfile(r, client.UserID) {
				clientList = append(clientList, client)
			}
		}
	}

	// Enrich with override information
	result := make([]ClientWithOverrides, len(clientList))
	for i, c := range clientList {
		hasOverrides := false
		if settings, err := h.settings.Get(c.ID); err == nil && settings != nil {
			hasOverrides = !settings.IsEmpty()
		}
		result[i] = ClientWithOverrides{
			Client:       c,
			HasOverrides: hasOverrides,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// Get handles GET /api/clients/{clientID}
func (h *ClientsHandler) Get(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(client)
}

// ClientUpdateRequest is the request body for updating a client
type ClientUpdateRequest struct {
	Name          *string `json:"name,omitempty"`
	Nickname      *string `json:"nickname,omitempty"`
	FilterEnabled *bool   `json:"filterEnabled,omitempty"`
}

// Update handles PUT /api/clients/{clientID}
// Updates client properties (name, filterEnabled)
func (h *ClientsHandler) Update(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	var req ClientUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Get current client
	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}

	// Apply updates
	if req.Name != nil {
		updated, err := h.clients.Rename(clientID, *req.Name)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		client = &updated
	}

	if req.Nickname != nil {
		updated, err := h.clients.SetNickname(clientID, *req.Nickname)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		client = &updated
	}

	if req.FilterEnabled != nil {
		updated, err := h.clients.SetFilterEnabled(clientID, *req.FilterEnabled)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		client = &updated
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(client)
}

// Delete handles DELETE /api/clients/{clientID}
func (h *ClientsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}

	// Also delete client settings
	if err := h.settings.Delete(clientID); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.clients.Delete(clientID); err != nil {
		if errors.Is(err, clients.ErrClientNotFound) {
			writeJSONError(w, "client not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetSettings handles GET /api/clients/{clientID}/settings
func (h *ClientsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Verify client exists
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}

	settings, err := h.settings.Get(clientID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return empty settings if none configured
	if settings == nil {
		settings = &models.ClientFilterSettings{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// UpdateSettings handles PUT /api/clients/{clientID}/settings
func (h *ClientsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Verify client exists
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}

	var settings models.ClientFilterSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validateClientFilterTerms("filtering", &settings); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.settings.Update(clientID, settings); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// ResetSettings handles DELETE /api/clients/{clientID}/settings
// Resets all client-specific settings to inherit from profile/global defaults
func (h *ClientsHandler) ResetSettings(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Verify client exists
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}

	if err := h.settings.Delete(clientID); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Client settings reset to defaults",
	})
}

// Ping handles POST /api/clients/{clientID}/ping
// Sets a pending ping for the client (called from admin UI to identify a device)
func (h *ClientsHandler) Ping(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	// Verify client exists
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}

	// Set pending ping
	h.pingMu.Lock()
	h.pendingPings[clientID] = pendingPing{timestamp: time.Now()}
	h.pingMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"clientId": clientID,
		"message":  "Ping sent to client",
	})
}

// CheckPing handles GET /api/clients/{clientID}/ping
// Checks if there's a pending ping for this client (called by the app)
// Returns and clears the ping if present
func (h *ClientsHandler) CheckPing(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}

	h.pingMu.Lock()
	ping, exists := h.pendingPings[clientID]
	hasPing := exists && time.Since(ping.timestamp) < pingExpiry
	if hasPing {
		delete(h.pendingPings, clientID) // Clear the ping once checked
	}
	h.pingMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ping": hasPing,
	})
}

type SendClientMessageRequest struct {
	Message    string   `json:"message"`
	TargetAll  bool     `json:"targetAll"`
	ProfileIDs []string `json:"profileIds"`
}

type ClientMessageResponse struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// SendMessage handles POST /api/clients/messages.
// It queues a short-lived popup message for all profiles or selected profiles.
func (h *ClientsHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	var req SendClientMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeJSONError(w, "message is required", http.StatusBadRequest)
		return
	}
	if len(message) > clientMessageMaxLength {
		writeJSONError(w, "message is too long", http.StatusBadRequest)
		return
	}

	profileIDs := make(map[string]struct{})
	for _, id := range req.ProfileIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		profileIDs[id] = struct{}{}
	}
	if !req.TargetAll && len(profileIDs) == 0 {
		writeJSONError(w, "select at least one profile or send to all profiles", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	msg := pendingClientMessage{
		ID:                  now.Format("20060102150405.000000000"),
		Message:             message,
		TargetAll:           req.TargetAll,
		ProfileIDs:          profileIDs,
		TargetProfileCount:  targetProfileCount(req.TargetAll, profileIDs),
		CreatedAt:           now,
		DeliveredProfileIDs: make(map[string]struct{}),
	}

	h.messageMu.Lock()
	h.pruneExpiredMessagesLocked(now)
	h.pendingMessages = append(h.pendingMessages, msg)
	h.messageMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": ClientMessageResponse{
			ID:        msg.ID,
			Message:   msg.Message,
			CreatedAt: msg.CreatedAt,
		},
	})
}

// CheckMessages handles GET /api/clients/{clientID}/messages?profileId=...
// It returns and clears messages that match this client/profile pair.
func (h *ClientsHandler) CheckMessages(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}
	if profileID == "" {
		writeJSONError(w, "profileId is required", http.StatusBadRequest)
		return
	}
	client, ok := h.getAuthorizedClient(w, r, clientID)
	if !ok {
		return
	}
	if client.UserID != profileID || !h.canAccessProfile(r, profileID) {
		writeJSONError(w, "client not found", http.StatusNotFound)
		return
	}

	now := time.Now().UTC()
	messages := make([]ClientMessageResponse, 0)

	h.messageMu.Lock()
	h.pruneExpiredMessagesLocked(now)
	for i := range h.pendingMessages {
		msg := &h.pendingMessages[i]
		if _, delivered := msg.DeliveredProfileIDs[profileID]; delivered {
			continue
		}
		if !msg.TargetAll {
			if _, ok := msg.ProfileIDs[profileID]; !ok {
				continue
			}
		}
		msg.DeliveredProfileIDs[profileID] = struct{}{}
		messages = append(messages, ClientMessageResponse{
			ID:        msg.ID,
			Message:   msg.Message,
			CreatedAt: msg.CreatedAt,
		})
	}
	h.pruneDeliveredMessagesLocked()
	h.messageMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": messages,
	})
}

func (h *ClientsHandler) pruneExpiredMessagesLocked(now time.Time) {
	if len(h.pendingMessages) == 0 {
		return
	}
	next := h.pendingMessages[:0]
	for _, msg := range h.pendingMessages {
		if now.Sub(msg.CreatedAt) <= clientMessageExpiry {
			next = append(next, msg)
		}
	}
	h.pendingMessages = next
}

func (h *ClientsHandler) pruneDeliveredMessagesLocked() {
	if len(h.pendingMessages) == 0 {
		return
	}
	next := h.pendingMessages[:0]
	for _, msg := range h.pendingMessages {
		if msg.TargetProfileCount > 0 && len(msg.DeliveredProfileIDs) >= msg.TargetProfileCount {
			continue
		}
		next = append(next, msg)
	}
	h.pendingMessages = next
}

func targetProfileCount(targetAll bool, profileIDs map[string]struct{}) int {
	if targetAll {
		return 0
	}
	return len(profileIDs)
}

// ReassignRequest is the request body for reassigning a client to a different profile
type ReassignRequest struct {
	UserID string `json:"userId"`
}

// Reassign handles POST /api/clients/{clientID}/reassign
// Reassigns a client to a different profile/user
func (h *ClientsHandler) Reassign(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := strings.TrimSpace(vars["clientID"])
	if clientID == "" {
		writeJSONError(w, "client id is required", http.StatusBadRequest)
		return
	}

	var req ReassignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.UserID) == "" {
		writeJSONError(w, "userId is required", http.StatusBadRequest)
		return
	}
	if _, ok := h.getAuthorizedClient(w, r, clientID); !ok {
		return
	}
	if !h.canAccessProfile(r, req.UserID) {
		writeJSONError(w, "user not found", http.StatusNotFound)
		return
	}

	client, err := h.clients.ReassignUser(clientID, req.UserID)
	if err != nil {
		if errors.Is(err, clients.ErrClientNotFound) {
			writeJSONError(w, "client not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(client)
}

// Options handles OPTIONS requests for CORS
func (h *ClientsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// writeJSONError writes a JSON error response
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
