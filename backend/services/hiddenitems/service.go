package hiddenitems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/internal/datastore"
	"novastream/internal/mediaidentity"
	"novastream/models"
)

var (
	ErrStorageDirRequired = errors.New("storage directory not provided")
	ErrUserIDRequired     = errors.New("user id is required")
	ErrIDRequired         = errors.New("id is required")
	ErrMediaTypeRequired  = errors.New("media type is required")
	ErrIdentifierRequired = errors.New("id and media type are required")
)

// Service persists profile-scoped hidden items.
type Service struct {
	mu     sync.RWMutex
	path   string
	store  *datastore.DataStore
	items  map[string]map[string]models.HiddenItem
	tokens map[string]map[string]map[string]struct{}
}

func NewServiceWithStore(store *datastore.DataStore) (*Service, error) {
	svc := &Service{
		store:  store,
		items:  make(map[string]map[string]models.HiddenItem),
		tokens: make(map[string]map[string]map[string]struct{}),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

func NewService(storageDir string) (*Service, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, ErrStorageDirRequired
	}
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create hidden items dir: %w", err)
	}
	svc := &Service{
		path:   filepath.Join(storageDir, "hidden_items.json"),
		items:  make(map[string]map[string]models.HiddenItem),
		tokens: make(map[string]map[string]map[string]struct{}),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) useDB() bool { return s.store != nil }

func (s *Service) List(userID string) ([]models.HiddenItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	perUser := s.items[userID]
	items := make([]models.HiddenItem, 0, len(perUser))
	for _, item := range perUser {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].HiddenAt.Equal(items[j].HiddenAt) {
			return items[i].Key() < items[j].Key()
		}
		return items[i].HiddenAt.After(items[j].HiddenAt)
	})
	return items, nil
}

func (s *Service) Hide(userID string, input models.HiddenItemUpsert) (models.HiddenItem, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.HiddenItem{}, ErrUserIDRequired
	}
	if strings.TrimSpace(input.ID) == "" {
		return models.HiddenItem{}, ErrIDRequired
	}
	if strings.TrimSpace(input.MediaType) == "" {
		return models.HiddenItem{}, ErrMediaTypeRequired
	}

	item := normalise(models.HiddenItem{
		ID:          input.ID,
		MediaType:   input.MediaType,
		Name:        input.Name,
		Year:        input.Year,
		PosterURL:   input.PosterURL,
		BackdropURL: input.BackdropURL,
		ExternalIDs: input.ExternalIDs,
		HiddenAt:    time.Now().UTC(),
	})

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser := s.ensureUserLocked(userID)
	if existing, ok := s.findLocked(userID, item.MediaType, item.ID, item.ExternalIDs); ok {
		delete(perUser, existing.Key())
		item = merge(existing, item)
	}
	perUser[item.Key()] = item
	s.rebuildUserIndexLocked(userID)

	if err := s.saveLocked(); err != nil {
		return models.HiddenItem{}, err
	}
	return item, nil
}

func (s *Service) Unhide(userID, mediaType, id string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, ErrUserIDRequired
	}
	mediaType = mediaidentity.NormalizeMediaType(mediaType)
	id = strings.TrimSpace(id)
	if mediaType == "" || id == "" {
		return false, ErrIdentifierRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	perUser, ok := s.items[userID]
	if !ok {
		return false, nil
	}

	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: id})
	removed := false
	for _, key := range identity.CandidateKeys {
		if _, exists := perUser[key]; exists {
			delete(perUser, key)
			removed = true
		}
	}
	for key, item := range perUser {
		if matches(item, mediaType, id, nil) {
			delete(perUser, key)
			removed = true
		}
	}
	if !removed {
		return false, nil
	}
	if len(perUser) == 0 {
		delete(s.items, userID)
		delete(s.tokens, userID)
	} else {
		s.rebuildUserIndexLocked(userID)
	}
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) IsHidden(userID, mediaType, id string, externalIDs map[string]string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	mediaType = mediaidentity.NormalizeMediaType(mediaType)
	id = strings.TrimSpace(id)
	if mediaType == "" || (id == "" && len(externalIDs) == 0) {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.findLocked(userID, mediaType, id, externalIDs)
	return ok
}

func (s *Service) FilterHiddenWatchlistItems(userID string, items []models.WatchlistItem) []models.WatchlistItem {
	if len(items) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if s.IsHidden(userID, item.MediaType, item.ID, item.ExternalIDs) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (s *Service) ShouldHideTitleMap(userID string, item map[string]interface{}) bool {
	mediaType, _ := stringValue(item["mediaType"])
	id, _ := stringValue(item["id"])
	externalIDs := externalIDsFromMap(item)
	if tmdbID, ok := numericString(item["tmdbId"]); ok && externalIDs["tmdb"] == "" {
		externalIDs["tmdb"] = tmdbID
	}
	if tvdbID, ok := numericString(item["tvdbId"]); ok && externalIDs["tvdb"] == "" {
		externalIDs["tvdb"] = tvdbID
	}
	if imdbID, ok := stringValue(item["imdbId"]); ok && externalIDs["imdb"] == "" {
		externalIDs["imdb"] = imdbID
	}
	return s.IsHidden(userID, mediaType, id, externalIDs)
}

func (s *Service) findLocked(userID, mediaType, id string, externalIDs map[string]string) (models.HiddenItem, bool) {
	perUser, ok := s.items[userID]
	if !ok {
		return models.HiddenItem{}, false
	}
	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: id, ExternalIDs: externalIDs})
	for _, key := range identity.CandidateKeys {
		if item, exists := perUser[key]; exists {
			return item, true
		}
	}
	userTokens := s.tokens[userID]
	for token := range identity.Tokens {
		if keys := userTokens[token]; len(keys) > 0 {
			for key := range keys {
				if item, exists := perUser[key]; exists {
					return item, true
				}
			}
		}
	}
	for _, item := range perUser {
		if matches(item, mediaType, id, externalIDs) {
			return item, true
		}
	}
	return models.HiddenItem{}, false
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useDB() {
		all, err := s.store.HiddenItems().ListAll(context.Background())
		if err != nil {
			return fmt.Errorf("load hidden items from db: %w", err)
		}
		s.items = make(map[string]map[string]models.HiddenItem, len(all))
		for userID, items := range all {
			perUser := make(map[string]models.HiddenItem, len(items))
			for _, item := range items {
				item = normalise(item)
				perUser[item.Key()] = item
			}
			s.items[userID] = perUser
		}
		s.rebuildIndexLocked()
		return nil
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open hidden items: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read hidden items: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var loaded map[string][]models.HiddenItem
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("decode hidden items: %w", err)
	}
	s.items = make(map[string]map[string]models.HiddenItem, len(loaded))
	for userID, items := range loaded {
		perUser := make(map[string]models.HiddenItem, len(items))
		for _, item := range items {
			item = normalise(item)
			perUser[item.Key()] = item
		}
		s.items[userID] = perUser
	}
	s.rebuildIndexLocked()
	return nil
}

func (s *Service) saveLocked() error {
	if s.useDB() {
		ctx := context.Background()
		return s.store.WithTx(ctx, func(tx *datastore.Tx) error {
			existing, err := tx.HiddenItems().ListAll(ctx)
			if err != nil {
				return err
			}
			for userID, perUser := range s.items {
				items := make([]models.HiddenItem, 0, len(perUser))
				for _, item := range perUser {
					items = append(items, item)
				}
				if err := tx.HiddenItems().BulkUpsert(ctx, userID, items); err != nil {
					return err
				}
				for _, item := range existing[userID] {
					if _, ok := perUser[item.Key()]; !ok {
						if err := tx.HiddenItems().Delete(ctx, userID, item.Key()); err != nil {
							return err
						}
					}
				}
				delete(existing, userID)
			}
			for userID := range existing {
				if err := tx.HiddenItems().DeleteByUser(ctx, userID); err != nil {
					return err
				}
			}
			return nil
		})
	}

	byUser := make(map[string][]models.HiddenItem, len(s.items))
	for userID, perUser := range s.items {
		items := make([]models.HiddenItem, 0, len(perUser))
		for _, item := range perUser {
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].HiddenAt.Before(items[j].HiddenAt) })
		byUser[userID] = items
	}
	tmp := s.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create hidden items temp file: %w", err)
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(byUser); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode hidden items: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync hidden items: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close hidden items temp file: %w", err)
	}
	return os.Rename(tmp, s.path)
}

func (s *Service) ensureUserLocked(userID string) map[string]models.HiddenItem {
	perUser, ok := s.items[userID]
	if !ok {
		perUser = make(map[string]models.HiddenItem)
		s.items[userID] = perUser
	}
	return perUser
}

func (s *Service) rebuildIndexLocked() {
	s.tokens = make(map[string]map[string]map[string]struct{}, len(s.items))
	for userID := range s.items {
		s.rebuildUserIndexLocked(userID)
	}
}

func (s *Service) rebuildUserIndexLocked(userID string) {
	perUser := s.items[userID]
	if len(perUser) == 0 {
		delete(s.tokens, userID)
		return
	}
	userTokens := make(map[string]map[string]struct{})
	for key, item := range perUser {
		identity := mediaidentity.Resolve(mediaidentity.Input{
			MediaType:   item.MediaType,
			ID:          item.ID,
			ExternalIDs: item.ExternalIDs,
		})
		for token := range identity.Tokens {
			if userTokens[token] == nil {
				userTokens[token] = make(map[string]struct{})
			}
			userTokens[token][key] = struct{}{}
		}
	}
	s.tokens[userID] = userTokens
}

func normalise(item models.HiddenItem) models.HiddenItem {
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   item.MediaType,
		ID:          item.ID,
		ExternalIDs: item.ExternalIDs,
	})
	item.MediaType = identity.MediaType
	item.ID = identity.ID
	item.ExternalIDs = identity.ExternalIDs
	if item.HiddenAt.IsZero() {
		item.HiddenAt = time.Now().UTC()
	}
	return item
}

func matches(item models.HiddenItem, mediaType, id string, externalIDs map[string]string) bool {
	identity := mediaidentity.Resolve(mediaidentity.Input{MediaType: mediaType, ID: id, ExternalIDs: externalIDs})
	for _, key := range identity.CandidateKeys {
		if key == item.Key() {
			return true
		}
	}
	itemIdentity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   item.MediaType,
		ID:          item.ID,
		ExternalIDs: item.ExternalIDs,
	})
	for token := range identity.Tokens {
		if _, ok := itemIdentity.Tokens[token]; ok {
			return true
		}
	}
	return false
}

func merge(existing, incoming models.HiddenItem) models.HiddenItem {
	if incoming.Name == "" {
		incoming.Name = existing.Name
	}
	if incoming.Year == 0 {
		incoming.Year = existing.Year
	}
	if incoming.PosterURL == "" {
		incoming.PosterURL = existing.PosterURL
	}
	if incoming.BackdropURL == "" {
		incoming.BackdropURL = existing.BackdropURL
	}
	incoming.ExternalIDs = mediaidentity.MergeExternalIDs(existing.ExternalIDs, incoming.ExternalIDs)
	return normalise(incoming)
}

func stringValue(value interface{}) (string, bool) {
	s, ok := value.(string)
	return strings.TrimSpace(s), ok && strings.TrimSpace(s) != ""
}

func numericString(value interface{}) (string, bool) {
	switch v := value.(type) {
	case float64:
		if v > 0 {
			return fmt.Sprintf("%.0f", v), true
		}
	case int:
		if v > 0 {
			return fmt.Sprintf("%d", v), true
		}
	case string:
		return stringValue(v)
	}
	return "", false
}

func externalIDsFromMap(item map[string]interface{}) map[string]string {
	ids := make(map[string]string)
	raw, _ := item["externalIds"].(map[string]interface{})
	for key, value := range raw {
		if s, ok := stringValue(value); ok {
			ids[key] = s
		}
	}
	return ids
}
