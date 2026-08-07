package client_settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrClientIDRequired   = errors.New("client id is required")
	ErrUserIDRequired     = errors.New("user id is required")
)

// Service manages persistence of per-(device, person) filter settings.
// In-memory and JSON keys use models.ClientSettingsKey(clientID, userID).
type Service struct {
	mu       sync.RWMutex
	path     string
	store    *datastore.DataStore
	settings map[string]models.ClientFilterSettings
}

func sanitizeAllowedTrackLanguages(settings *models.ClientFilterSettings) {
	if settings.AllowedTrackLanguages == nil {
		return
	}
	seen := make(map[string]struct{}, len(*settings.AllowedTrackLanguages))
	cleaned := make([]string, 0, len(*settings.AllowedTrackLanguages))
	for _, raw := range *settings.AllowedTrackLanguages {
		code := strings.ToLower(strings.TrimSpace(strings.Trim(raw, "'\"")))
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		cleaned = append(cleaned, code)
	}
	settings.AllowedTrackLanguages = &cleaned
}

func markNavigationVisibilityMigrated(settings *models.ClientFilterSettings) {
	if settings.NavigationTabVisibility == nil {
		return
	}
	migrated := true
	settings.NavigationTabVisibilityIncludesSystemTabs = &migrated
	settings.NavigationTabVisibilityIncludesWatchlist = &migrated
}

func (s *Service) useDB() bool { return s.store != nil }

// NewServiceWithStore creates a client settings service backed by PostgreSQL.
func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:    store,
		settings: make(map[string]models.ClientFilterSettings),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

// NewService creates a client settings service storing data inside the provided directory.
func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create client settings dir: %w", err)
	}

	svc := &Service{
		path:     filepath.Join(storageDir, "client_settings.json"),
		settings: make(map[string]models.ClientFilterSettings),
	}

	if err := svc.load(); err != nil {
		return nil, err
	}

	return svc, nil
}

// Get returns settings for a person×device pair, or nil if not set.
// Falls back to a legacy device-only key when no composite entry exists.
func (s *Service) Get(clientID, userID string) (*models.ClientFilterSettings, error) {
	clientID = strings.TrimSpace(clientID)
	userID = strings.TrimSpace(userID)
	if clientID == "" {
		return nil, ErrClientIDRequired
	}
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if settings, ok := s.settings[models.ClientSettingsKey(clientID, userID)]; ok {
		copy := settings
		return &copy, nil
	}
	// Legacy device-only key (pre multi-person migration)
	if settings, ok := s.settings[clientID]; ok {
		copy := settings
		return &copy, nil
	}

	return nil, nil
}

// Update saves settings for a person×device pair.
func (s *Service) Update(clientID, userID string, settings models.ClientFilterSettings) error {
	clientID = strings.TrimSpace(clientID)
	userID = strings.TrimSpace(userID)
	if clientID == "" {
		return ErrClientIDRequired
	}
	if userID == "" {
		return ErrUserIDRequired
	}
	sanitizeAllowedTrackLanguages(&settings)
	markNavigationVisibilityMigrated(&settings)

	s.mu.Lock()
	defer s.mu.Unlock()

	key := models.ClientSettingsKey(clientID, userID)
	// Prefer composite key; drop legacy device-only entry when writing composite.
	delete(s.settings, clientID)
	if settings.IsEmpty() {
		delete(s.settings, key)
	} else {
		s.settings[key] = settings
	}

	return s.saveLocked()
}

// GetAll returns a copy of all client settings (composite keys).
func (s *Service) GetAll() map[string]models.ClientFilterSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]models.ClientFilterSettings, len(s.settings))
	for k, v := range s.settings {
		out[k] = v
	}
	return out
}

// UpdateBatch replaces all client settings with the provided map and saves.
// Empty entries are removed. Keys should be composite; legacy keys are accepted.
func (s *Service) UpdateBatch(settings map[string]models.ClientFilterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleaned := make(map[string]models.ClientFilterSettings, len(settings))
	for k, v := range settings {
		sanitizeAllowedTrackLanguages(&v)
		markNavigationVisibilityMigrated(&v)
		if !v.IsEmpty() {
			cleaned[k] = v
		}
	}
	s.settings = cleaned
	return s.saveLocked()
}

// Delete removes settings for a person×device pair.
func (s *Service) Delete(clientID, userID string) error {
	clientID = strings.TrimSpace(clientID)
	userID = strings.TrimSpace(userID)
	if clientID == "" {
		return ErrClientIDRequired
	}
	if userID == "" {
		return ErrUserIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := models.ClientSettingsKey(clientID, userID)
	delete(s.settings, key)
	// Also clear legacy device-only key when deleting for any profile
	delete(s.settings, clientID)

	return s.saveLocked()
}

// DeleteByClient removes all person×device settings for a device.
func (s *Service) DeleteByClient(clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ErrClientIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.settings, clientID)
	prefix := clientID + models.ClientSettingsKeySep
	for k := range s.settings {
		if strings.HasPrefix(k, prefix) {
			delete(s.settings, k)
		}
	}

	return s.saveLocked()
}

// Move reassigns settings from one profile to another for the same device.
// If the destination already has settings, the source is dropped without overwrite.
func (s *Service) Move(clientID, fromUserID, toUserID string) error {
	clientID = strings.TrimSpace(clientID)
	fromUserID = strings.TrimSpace(fromUserID)
	toUserID = strings.TrimSpace(toUserID)
	if clientID == "" {
		return ErrClientIDRequired
	}
	if fromUserID == "" || toUserID == "" {
		return ErrUserIDRequired
	}
	if fromUserID == toUserID {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fromKey := models.ClientSettingsKey(clientID, fromUserID)
	toKey := models.ClientSettingsKey(clientID, toUserID)

	src, ok := s.settings[fromKey]
	if !ok {
		// Legacy device-only
		if legacy, lok := s.settings[clientID]; lok {
			src = legacy
			ok = true
			delete(s.settings, clientID)
		}
	}
	if !ok {
		return nil
	}
	delete(s.settings, fromKey)
	if _, exists := s.settings[toKey]; !exists {
		s.settings[toKey] = src
	}
	return s.saveLocked()
}

// ClearAppearanceOverrides removes client-level display appearance overrides.
func (s *Service) ClearAppearanceOverrides() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := 0
	for key, settings := range s.settings {
		if settings.Appearance == nil {
			continue
		}
		settings.Appearance = nil
		if settings.IsEmpty() {
			delete(s.settings, key)
		} else {
			s.settings[key] = settings
		}
		changed++
	}

	if changed == 0 {
		return 0, nil
	}
	return changed, s.saveLocked()
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		settings, err := s.store.ClientSettings().List(context.Background())
		if err != nil {
			return fmt.Errorf("load client settings from db: %w", err)
		}
		needsSave := normalizeNavigationTabVisibility(settings)
		s.settings = settings
		if needsSave {
			if err := s.saveLocked(); err != nil {
				return fmt.Errorf("persist client settings migration: %w", err)
			}
		}
		return nil
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.settings = make(map[string]models.ClientFilterSettings)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open client settings: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read client settings: %w", err)
	}
	if len(data) == 0 {
		s.settings = make(map[string]models.ClientFilterSettings)
		return nil
	}

	var settings map[string]models.ClientFilterSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("decode client settings: %w", err)
	}

	needsSave := normalizeNavigationTabVisibility(settings)
	s.settings = settings
	if needsSave {
		if err := s.saveLocked(); err != nil {
			return fmt.Errorf("persist client settings migration: %w", err)
		}
	}
	return nil
}

func normalizeNavigationTabVisibility(settings map[string]models.ClientFilterSettings) bool {
	needsSave := false
	for key, cs := range settings {
		changed := false
		if cs.NavigationTabVisibilityIncludesSystemTabs == nil || !*cs.NavigationTabVisibilityIncludesSystemTabs {
			if cs.NavigationTabVisibility != nil {
				if tabs, tabsChanged := models.AddMissingSystemNavigationTabs(*cs.NavigationTabVisibility); tabsChanged {
					cs.NavigationTabVisibility = &tabs
				}
			}
			migrated := true
			cs.NavigationTabVisibilityIncludesSystemTabs = &migrated
			changed = true
		}
		if cs.NavigationTabVisibilityIncludesWatchlist == nil || !*cs.NavigationTabVisibilityIncludesWatchlist {
			if cs.NavigationTabVisibility != nil {
				if tabs, tabsChanged := models.AddMissingWatchlistNavigationTab(*cs.NavigationTabVisibility); tabsChanged {
					cs.NavigationTabVisibility = &tabs
				}
			}
			migrated := true
			cs.NavigationTabVisibilityIncludesWatchlist = &migrated
			changed = true
		}
		if changed {
			settings[key] = cs
			needsSave = true
		}
	}
	return needsSave
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		return s.syncToDB()
	}

	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create client settings temp file: %w", err)
	}

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.settings); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode client settings: %w", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync client settings: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close client settings temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace client settings file: %w", err)
	}

	return nil
}

func (s *Service) syncToDB() error {
	ctx := context.Background()
	return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
		existing, err := tx.ClientSettings().List(ctx)
		if err != nil {
			return err
		}
		dbKeys := make(map[string]bool, len(existing))
		for id := range existing {
			dbKeys[id] = true
		}
		for key, settings := range s.settings {
			clientID, userID, ok := models.SplitClientSettingsKey(key)
			if !ok {
				// Skip legacy keys that cannot be written without a user
				continue
			}
			cs := settings
			if err := tx.ClientSettings().Upsert(ctx, clientID, userID, &cs); err != nil {
				return err
			}
			delete(dbKeys, key)
		}
		for key := range dbKeys {
			clientID, userID, ok := models.SplitClientSettingsKey(key)
			if !ok {
				continue
			}
			if err := tx.ClientSettings().Delete(ctx, clientID, userID); err != nil {
				return err
			}
		}
		return nil
	})
}
