package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrClientIDRequired   = errors.New("client id is required")
	ErrClientNotFound     = errors.New("client not found")
	ErrUserNotFound       = errors.New("user not found")
	ErrProfileNotLinked   = errors.New("device is not associated with that person")
)

// Service manages persistence of client devices and person×device associations.
type Service struct {
	mu       sync.RWMutex
	path     string
	store    *datastore.DataStore
	clients  map[string]models.Client
	// profiles: clientID -> userID -> association timestamps
	profiles map[string]map[string]models.ClientProfileAssociation
}

type clientsFileV2 struct {
	Clients  map[string]models.Client                               `json:"clients"`
	Profiles map[string]map[string]models.ClientProfileAssociation `json:"profiles"`
}

// useDB returns true when the service is backed by PostgreSQL.
func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a clients service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:    store,
		clients:  make(map[string]models.Client),
		profiles: make(map[string]map[string]models.ClientProfileAssociation),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

// NewService creates a clients service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create clients dir: %w", err)
	}

	svc := &Service{
		path:     filepath.Join(storageDir, "clients.json"),
		clients:  make(map[string]models.Client),
		profiles: make(map[string]map[string]models.ClientProfileAssociation),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// Register registers or updates a client device and records a person×device sighting.
// Existing associations with other profiles are retained.
func (s *Service) Register(id, userID, deviceType, os, appVersion, deviceName, nickname string) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	if s.useDB() && userID != "" {
		u, err := s.store.Users().Get(context.Background(), userID)
		if err != nil {
			return models.Client{}, fmt.Errorf("check user: %w", err)
		}
		if u == nil {
			return models.Client{}, ErrUserNotFound
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	nickname = strings.TrimSpace(nickname)
	if len([]rune(nickname)) > 64 {
		return models.Client{}, errors.New("nickname must be 64 characters or fewer")
	}

	if existing, ok := s.clients[id]; ok {
		if userID != "" {
			existing.UserID = userID
		}
		existing.DeviceName = deviceName
		existing.DeviceType = deviceType
		existing.OS = os
		existing.AppVersion = appVersion
		if nickname != "" {
			existing.Nickname = nickname
		}
		existing.LastSeenAt = now
		s.clients[id] = existing

		if userID != "" {
			s.touchProfileLocked(id, userID, now, false)
		}

		if err := s.saveLocked(); err != nil {
			return models.Client{}, err
		}
		return s.instanceLocked(id, userID), nil
	}

	if userID == "" {
		return models.Client{}, errors.New("userId is required for new client registration")
	}

	client := models.Client{
		ID:            id,
		UserID:        userID,
		Name:          generateClientName(deviceType, os),
		Nickname:      nickname,
		DeviceName:    deviceName,
		DeviceType:    deviceType,
		OS:            os,
		AppVersion:    appVersion,
		FirstSeenAt:   now,
		LastSeenAt:    now,
		FilterEnabled: false,
	}

	s.clients[id] = client
	s.touchProfileLocked(id, userID, now, true)

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return s.instanceLocked(id, userID), nil
}

func (s *Service) touchProfileLocked(clientID, userID string, now time.Time, isNewDevice bool) {
	if s.profiles[clientID] == nil {
		s.profiles[clientID] = make(map[string]models.ClientProfileAssociation)
	}
	if existing, ok := s.profiles[clientID][userID]; ok {
		existing.LastSeenAt = now
		s.profiles[clientID][userID] = existing
		return
	}
	first := now
	if isNewDevice {
		if c, ok := s.clients[clientID]; ok && !c.FirstSeenAt.IsZero() {
			first = c.FirstSeenAt
		}
	}
	s.profiles[clientID][userID] = models.ClientProfileAssociation{
		ClientID:    clientID,
		UserID:      userID,
		FirstSeenAt: first,
		LastSeenAt:  now,
	}
}

// instanceLocked returns a client row scoped to userID when known.
func (s *Service) instanceLocked(clientID, userID string) models.Client {
	c := s.clients[clientID]
	if userID == "" {
		return c
	}
	c.UserID = userID
	if assoc, ok := s.profiles[clientID][userID]; ok {
		c.FirstSeenAt = assoc.FirstSeenAt
		c.LastSeenAt = assoc.LastSeenAt
	}
	return c
}

// SetNickname updates the user-assigned name for a client. An empty nickname
// clears the custom label and lets administrative UIs fall back to Name.
func (s *Service) SetNickname(id, nickname string) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}
	nickname = strings.TrimSpace(nickname)
	if len([]rune(nickname)) > 64 {
		return models.Client{}, errors.New("nickname must be 64 characters or fewer")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return models.Client{}, ErrClientNotFound
	}
	client.Nickname = nickname
	s.clients[id] = client
	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}
	return client, nil
}

// generateClientName creates a display name like "iPhone - iOS" or "Apple TV - tvOS"
func generateClientName(deviceType, os string) string {
	if deviceType == "" && os == "" {
		return "Unknown Device"
	}
	if deviceType == "" {
		return os
	}
	if os == "" {
		return deviceType
	}
	return fmt.Sprintf("%s - %s", deviceType, os)
}

// Get returns a client by ID (device row; UserID is last-active profile).
func (s *Service) Get(id string) (*models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrClientIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if client, ok := s.clients[id]; ok {
		copy := client
		return &copy, nil
	}

	return nil, nil
}

// HasProfile reports whether the device has been associated with the given profile.
func (s *Service) HasProfile(clientID, userID string) bool {
	clientID = strings.TrimSpace(clientID)
	userID = strings.TrimSpace(userID)
	if clientID == "" || userID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	assocs, ok := s.profiles[clientID]
	if !ok {
		// Fallback: exclusive last-active user
		if c, ok := s.clients[clientID]; ok {
			return c.UserID == userID
		}
		return false
	}
	_, ok = assocs[userID]
	return ok
}

// ProfileIDs returns all profiles associated with a device.
func (s *Service) ProfileIDs(clientID string) []string {
	clientID = strings.TrimSpace(clientID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	assocs := s.profiles[clientID]
	if len(assocs) == 0 {
		if c, ok := s.clients[clientID]; ok && c.UserID != "" {
			return []string{c.UserID}
		}
		return nil
	}
	ids := make([]string, 0, len(assocs))
	for uid := range assocs {
		ids = append(ids, uid)
	}
	sort.Strings(ids)
	return ids
}

// List returns person×device instances (one row per association), most recent first.
// Devices with no associations fall back to a single row using clients.user_id.
func (s *Service) List() []models.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make([]models.Client, 0)
	for id, c := range s.clients {
		assocs := s.profiles[id]
		if len(assocs) == 0 {
			clients = append(clients, c)
			continue
		}
		for userID := range assocs {
			clients = append(clients, s.instanceLocked(id, userID))
		}
	}

	sort.Slice(clients, func(i, j int) bool {
		return clients[i].LastSeenAt.After(clients[j].LastSeenAt)
	})

	return clients
}

// ListByUser returns all device instances associated with a profile.
func (s *Service) ListByUser(userID string) []models.Client {
	userID = strings.TrimSpace(userID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make([]models.Client, 0)
	for id, assocs := range s.profiles {
		if _, ok := assocs[userID]; !ok {
			continue
		}
		if _, ok := s.clients[id]; !ok {
			continue
		}
		clients = append(clients, s.instanceLocked(id, userID))
	}
	// Fallback for devices with no profile rows yet
	if len(clients) == 0 {
		for _, c := range s.clients {
			if c.UserID == userID {
				if _, has := s.profiles[c.ID]; has {
					continue
				}
				clients = append(clients, c)
			}
		}
	}

	sort.Slice(clients, func(i, j int) bool {
		return clients[i].LastSeenAt.After(clients[j].LastSeenAt)
	})

	return clients
}

// Rename updates a client's display name.
func (s *Service) Rename(id, name string) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return models.Client{}, errors.New("name is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return models.Client{}, ErrClientNotFound
	}

	client.Name = name
	s.clients[id] = client

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return client, nil
}

// SetFilterEnabled enables or disables custom filtering for a client.
func (s *Service) SetFilterEnabled(id string, enabled bool) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return models.Client{}, ErrClientNotFound
	}

	client.FilterEnabled = enabled
	s.clients[id] = client

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return client, nil
}

// UpdateLastSeen updates the last seen timestamp for a client and optional profile.
func (s *Service) UpdateLastSeen(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return nil
	}

	now := time.Now().UTC()
	client.LastSeenAt = now
	s.clients[id] = client
	if client.UserID != "" {
		s.touchProfileLocked(id, client.UserID, now, false)
	}

	return s.saveLocked()
}

// Delete removes a client device and all associations.
func (s *Service) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.clients[id]; !ok {
		return ErrClientNotFound
	}

	delete(s.clients, id)
	delete(s.profiles, id)

	return s.saveLocked()
}

// UnassignProfile removes a person×device association. If no associations remain,
// the device row is deleted.
func (s *Service) UnassignProfile(id, userID string) (deletedDevice bool, err error) {
	id = strings.TrimSpace(id)
	userID = strings.TrimSpace(userID)
	if id == "" {
		return false, ErrClientIDRequired
	}
	if userID == "" {
		return false, errors.New("user id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return false, ErrClientNotFound
	}

	assocs := s.profiles[id]
	if assocs == nil {
		if client.UserID != userID {
			return false, ErrProfileNotLinked
		}
		delete(s.clients, id)
		return true, s.saveLocked()
	}
	if _, ok := assocs[userID]; !ok {
		return false, ErrProfileNotLinked
	}
	delete(assocs, userID)
	if len(assocs) == 0 {
		delete(s.profiles, id)
		delete(s.clients, id)
		return true, s.saveLocked()
	}
	s.profiles[id] = assocs
	// Keep last-active pointer valid
	if client.UserID == userID {
		var latestUID string
		var latest time.Time
		for uid, a := range assocs {
			if latestUID == "" || a.LastSeenAt.After(latest) {
				latestUID = uid
				latest = a.LastSeenAt
			}
		}
		client.UserID = latestUID
		s.clients[id] = client
	}
	return false, s.saveLocked()
}

// ReassignUser moves a device association from fromUserID to newUserID.
// When fromUserID is empty, uses the device's last-active UserID.
// Settings migration is handled by the caller (handlers).
func (s *Service) ReassignUser(id, fromUserID, newUserID string) (models.Client, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.Client{}, ErrClientIDRequired
	}

	newUserID = strings.TrimSpace(newUserID)
	if newUserID == "" {
		return models.Client{}, errors.New("new user ID is required")
	}
	fromUserID = strings.TrimSpace(fromUserID)

	if s.useDB() {
		u, err := s.store.Users().Get(context.Background(), newUserID)
		if err != nil {
			return models.Client{}, fmt.Errorf("check user: %w", err)
		}
		if u == nil {
			return models.Client{}, ErrUserNotFound
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return models.Client{}, ErrClientNotFound
	}

	if fromUserID == "" {
		fromUserID = client.UserID
	}
	if fromUserID == "" {
		return models.Client{}, errors.New("source user ID is required")
	}
	if fromUserID == newUserID {
		return s.instanceLocked(id, newUserID), nil
	}

	now := time.Now().UTC()
	// Ensure source association exists (or fall back to exclusive ownership)
	if assocs := s.profiles[id]; assocs != nil {
		if _, ok := assocs[fromUserID]; !ok && client.UserID != fromUserID {
			return models.Client{}, ErrProfileNotLinked
		}
	} else if client.UserID != fromUserID {
		return models.Client{}, ErrProfileNotLinked
	}

	// Capture first-seen from source when present
	first := now
	if assocs := s.profiles[id]; assocs != nil {
		if src, ok := assocs[fromUserID]; ok {
			first = src.FirstSeenAt
			delete(assocs, fromUserID)
		}
	}
	if s.profiles[id] == nil {
		s.profiles[id] = make(map[string]models.ClientProfileAssociation)
	}
	if dest, ok := s.profiles[id][newUserID]; ok {
		// Keep earlier first-seen
		if dest.FirstSeenAt.Before(first) {
			first = dest.FirstSeenAt
		}
	}
	s.profiles[id][newUserID] = models.ClientProfileAssociation{
		ClientID:    id,
		UserID:      newUserID,
		FirstSeenAt: first,
		LastSeenAt:  now,
	}

	client.UserID = newUserID
	client.LastSeenAt = now
	s.clients[id] = client

	if err := s.saveLocked(); err != nil {
		return models.Client{}, err
	}

	return s.instanceLocked(id, newUserID), nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		clients, err := s.store.Clients().List(context.Background())
		if err != nil {
			return fmt.Errorf("load clients from db: %w", err)
		}
		s.clients = make(map[string]models.Client, len(clients))
		for _, c := range clients {
			// List may expand; store unique device rows with last-active user
			if existing, ok := s.clients[c.ID]; ok {
				if c.LastSeenAt.After(existing.LastSeenAt) {
					// Prefer fresher last-active pointer from exclusive column when loading
					// from expanded list is ambiguous; re-load raw below if needed.
					_ = existing
				}
			}
			// DB List() returns raw devices (not expanded) — ListProfiles separate.
			s.clients[c.ID] = c
		}
		// Raw device list from DB does not expand; reload without join if we used expanded.
		// pg List still returns one row per device from clients table.
		assocs, err := s.store.Clients().ListProfiles(context.Background())
		if err != nil {
			return fmt.Errorf("load client profiles from db: %w", err)
		}
		s.profiles = make(map[string]map[string]models.ClientProfileAssociation)
		for _, a := range assocs {
			if s.profiles[a.ClientID] == nil {
				s.profiles[a.ClientID] = make(map[string]models.ClientProfileAssociation)
			}
			s.profiles[a.ClientID][a.UserID] = a
		}
		// Ensure every device has at least its last-active association
		for id, c := range s.clients {
			if c.UserID == "" {
				continue
			}
			if s.profiles[id] == nil {
				s.profiles[id] = make(map[string]models.ClientProfileAssociation)
			}
			if _, ok := s.profiles[id][c.UserID]; !ok {
				s.profiles[id][c.UserID] = models.ClientProfileAssociation{
					ClientID:    id,
					UserID:      c.UserID,
					FirstSeenAt: c.FirstSeenAt,
					LastSeenAt:  c.LastSeenAt,
				}
			}
		}
		return nil
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.clients = make(map[string]models.Client)
		s.profiles = make(map[string]map[string]models.ClientProfileAssociation)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open clients file: %w", err)
	}
	defer file.Close()

	var raw json.RawMessage
	if err := json.NewDecoder(file).Decode(&raw); err != nil {
		return fmt.Errorf("decode clients: %w", err)
	}

	// v2 format: { clients, profiles }
	var v2 clientsFileV2
	if err := json.Unmarshal(raw, &v2); err == nil && v2.Clients != nil {
		s.clients = v2.Clients
		if v2.Profiles != nil {
			s.profiles = v2.Profiles
		} else {
			s.profiles = make(map[string]map[string]models.ClientProfileAssociation)
		}
	} else {
		// Legacy: flat map of clients
		var legacy map[string]models.Client
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return fmt.Errorf("decode clients: %w", err)
		}
		s.clients = legacy
		s.profiles = make(map[string]map[string]models.ClientProfileAssociation)
	}

	// Seed associations from exclusive user_id when missing
	for id, c := range s.clients {
		if c.UserID == "" {
			continue
		}
		if s.profiles[id] == nil {
			s.profiles[id] = make(map[string]models.ClientProfileAssociation)
		}
		if _, ok := s.profiles[id][c.UserID]; !ok {
			s.profiles[id][c.UserID] = models.ClientProfileAssociation{
				ClientID:    id,
				UserID:      c.UserID,
				FirstSeenAt: c.FirstSeenAt,
				LastSeenAt:  c.LastSeenAt,
			}
		}
	}
	return nil
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	payload := clientsFileV2{
		Clients:  s.clients,
		Profiles: s.profiles,
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create clients temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode clients: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync clients: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close clients temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace clients file: %w", err)
	}

	return nil
}

func (s *Service) pruneInvalidClientsLocked(validUserIDs map[string]struct{}) []models.Client {
	var removed []models.Client
	for id, c := range s.clients {
		// Drop associations to missing users
		if assocs := s.profiles[id]; assocs != nil {
			for uid := range assocs {
				if _, ok := validUserIDs[uid]; !ok {
					delete(assocs, uid)
				}
			}
			if len(assocs) == 0 {
				delete(s.profiles, id)
			} else {
				s.profiles[id] = assocs
			}
		}
		// Drop device if last-active user invalid and no associations remain
		if _, ok := validUserIDs[c.UserID]; ok {
			continue
		}
		if assocs := s.profiles[id]; len(assocs) > 0 {
			// Point last-active at any remaining association
			for uid, a := range assocs {
				c.UserID = uid
				c.LastSeenAt = a.LastSeenAt
				s.clients[id] = c
				break
			}
			continue
		}
		delete(s.clients, id)
		delete(s.profiles, id)
		removed = append(removed, c)
	}
	return removed
}

// syncToDB writes the full in-memory clients state to PostgreSQL.
func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		users, err := tx.Users().List(ctx)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		validUserIDs := make(map[string]struct{}, len(users))
		for _, u := range users {
			validUserIDs[u.ID] = struct{}{}
		}
		for _, c := range s.pruneInvalidClientsLocked(validUserIDs) {
			log.Printf("[clients] pruning orphaned client %q for missing user %q", c.ID, c.UserID)
		}

		existing, err := tx.Clients().List(ctx)
		if err != nil {
			return err
		}
		dbIDs := make(map[string]bool, len(existing))
		for _, c := range existing {
			dbIDs[c.ID] = true
		}
		for _, c := range s.clients {
			client := c
			if dbIDs[c.ID] {
				if err := tx.Clients().Update(ctx, &client); err != nil {
					return err
				}
			} else {
				if err := tx.Clients().Create(ctx, &client); err != nil {
					return err
				}
			}
			delete(dbIDs, c.ID)
		}
		for id := range dbIDs {
			if err := tx.Clients().Delete(ctx, id); err != nil {
				return err
			}
		}

		// Sync associations: replace all for simplicity
		existingAssocs, err := tx.Clients().ListProfiles(ctx)
		if err != nil {
			return err
		}
		type pair struct{ c, u string }
		want := make(map[pair]models.ClientProfileAssociation)
		for clientID, assocs := range s.profiles {
			for userID, a := range assocs {
				a.ClientID = clientID
				a.UserID = userID
				want[pair{clientID, userID}] = a
			}
		}
		have := make(map[pair]bool, len(existingAssocs))
		for _, a := range existingAssocs {
			p := pair{a.ClientID, a.UserID}
			have[p] = true
			if _, ok := want[p]; !ok {
				if err := tx.Clients().DeleteProfile(ctx, a.ClientID, a.UserID); err != nil {
					return err
				}
			}
		}
		for p, a := range want {
			if err := tx.Clients().UpsertProfile(ctx, a); err != nil {
				return err
			}
			_ = p
		}
		return nil
	})
}
