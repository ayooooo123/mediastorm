package libraryaccess

import (
	"context"
	"errors"
	"strings"
	"sync"

	"novastream/internal/datastore"
	"novastream/models"
)

var ErrLibraryIDRequired = errors.New("library ID is required")

// Service is the shared authorization boundary for local, Plex, and Jellyfin libraries.
type Service struct {
	repo   datastore.LibraryAccessRepository
	local  localItemRepository
	remote remoteItemRepository
	mu     sync.RWMutex
	cache  map[string]models.LibraryAccessPolicy
	items  map[string]string
}

type localItemRepository interface {
	GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error)
}

type remoteItemRepository interface {
	GetItem(ctx context.Context, id string) (*models.RemoteMediaItem, error)
}

func New(repo datastore.LibraryAccessRepository, local localItemRepository, remote remoteItemRepository) *Service {
	return &Service{repo: repo, local: local, remote: remote, cache: make(map[string]models.LibraryAccessPolicy), items: make(map[string]string)}
}

func (s *Service) Get(ctx context.Context, libraryID string) (models.LibraryAccessPolicy, error) {
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return models.LibraryAccessPolicy{}, ErrLibraryIDRequired
	}
	s.mu.RLock()
	if policy, ok := s.cache[libraryID]; ok {
		s.mu.RUnlock()
		return clonePolicy(policy), nil
	}
	s.mu.RUnlock()
	policy, err := s.repo.Get(ctx, libraryID)
	if err != nil {
		return models.LibraryAccessPolicy{}, err
	}
	if policy == nil {
		restricted := models.LibraryAccessPolicy{LibraryID: libraryID, AccessMode: models.LibraryAccessModeRestricted, AllowedAccountIDs: []string{}, AllowedProfileIDs: []string{}}
		s.storeCached(restricted)
		return restricted, nil
	}
	policy.AllowedAccountIDs = normalizeIDs(policy.AllowedAccountIDs)
	policy.AllowedProfileIDs = normalizeIDs(policy.AllowedProfileIDs)
	s.storeCached(*policy)
	return *policy, nil
}

func (s *Service) List(ctx context.Context) (map[string]models.LibraryAccessPolicy, error) {
	policies, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	for libraryID, policy := range policies {
		s.cache[libraryID] = clonePolicy(policy)
	}
	s.mu.Unlock()
	return policies, nil
}

func (s *Service) Set(ctx context.Context, policy models.LibraryAccessPolicy) error {
	policy.LibraryID = strings.TrimSpace(policy.LibraryID)
	if policy.LibraryID == "" {
		return ErrLibraryIDRequired
	}
	if policy.AccessMode != models.LibraryAccessModeAll {
		policy.AccessMode = models.LibraryAccessModeRestricted
	}
	policy.AllowedAccountIDs = normalizeIDs(policy.AllowedAccountIDs)
	policy.AllowedProfileIDs = normalizeIDs(policy.AllowedProfileIDs)
	if err := s.repo.Set(ctx, policy); err != nil {
		return err
	}
	s.storeCached(policy)
	return nil
}

func (s *Service) Delete(ctx context.Context, libraryID string) error {
	libraryID = strings.TrimSpace(libraryID)
	if err := s.repo.Delete(ctx, libraryID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, libraryID)
	s.mu.Unlock()
	return nil
}

func (s *Service) CanAccess(ctx context.Context, libraryID, accountID, profileID string, master bool) (bool, error) {
	accountID = strings.TrimSpace(accountID)
	profileID = strings.TrimSpace(profileID)

	// Master admin context (no active profile) can always manage/access libraries.
	if master && profileID == "" {
		return true, nil
	}

	policy, err := s.Get(ctx, libraryID)
	if err != nil {
		return false, err
	}
	if policy.AccessMode == models.LibraryAccessModeAll {
		return true, nil
	}

	// Restricted with no selections: master household only. Matches admin UI copy
	// ("only the master account can use the library") and keeps the app usable
	// when a master profile is selected — the frontend always sends profileId.
	hasGrants := len(policy.AllowedAccountIDs) > 0 || len(policy.AllowedProfileIDs) > 0
	if !hasGrants {
		return master, nil
	}

	// Restricted with explicit grants: selected accounts/profiles only.
	// Master with an active profile that is not granted is denied so kids/guest
	// profiles on the master account can be kept out of a library.
	return contains(policy.AllowedAccountIDs, accountID) || contains(policy.AllowedProfileIDs, profileID), nil
}

// LibraryIDForItem resolves either a local or remote item to its owning library.
func (s *Service) LibraryIDForItem(ctx context.Context, itemID string) (string, error) {
	itemID = strings.TrimSpace(itemID)
	s.mu.RLock()
	if libraryID := s.items[itemID]; libraryID != "" {
		s.mu.RUnlock()
		return libraryID, nil
	}
	s.mu.RUnlock()
	if s.local != nil {
		item, err := s.local.GetItem(ctx, itemID)
		if err == nil && item != nil {
			s.storeItemLibrary(itemID, item.LibraryID)
			return item.LibraryID, nil
		}
	}
	if s.remote != nil {
		item, err := s.remote.GetItem(ctx, itemID)
		if err == nil && item != nil {
			s.storeItemLibrary(itemID, item.LibraryID)
			return item.LibraryID, nil
		}
	}
	return "", nil
}

// CanAccessStream recognizes library provider paths and authorizes their item.
// Non-library paths return recognized=false and are left to the normal video path.
func (s *Service) CanAccessStream(ctx context.Context, streamPath, accountID, profileID string, master bool) (recognized, allowed bool, err error) {
	raw := strings.TrimSpace(streamPath)
	prefixes := []string{"localmedia:", "plexmedia:", "jellyfinmedia:"}
	recognized = false
	for _, prefix := range prefixes {
		if strings.HasPrefix(raw, prefix) {
			raw = strings.TrimPrefix(raw, prefix)
			recognized = true
			break
		}
	}
	if !recognized {
		return false, false, nil
	}
	if slash := strings.IndexByte(raw, '/'); slash >= 0 {
		raw = raw[:slash]
	}
	libraryID, err := s.LibraryIDForItem(ctx, strings.TrimSpace(raw))
	if err != nil || libraryID == "" {
		return true, false, err
	}
	allowed, err = s.CanAccess(ctx, libraryID, accountID, profileID, master)
	return true, allowed, err
}

func normalizeIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func contains(values []string, target string) bool {
	if target == "" {
		return false
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func clonePolicy(policy models.LibraryAccessPolicy) models.LibraryAccessPolicy {
	policy.AllowedAccountIDs = append([]string(nil), policy.AllowedAccountIDs...)
	policy.AllowedProfileIDs = append([]string(nil), policy.AllowedProfileIDs...)
	return policy
}

func (s *Service) storeCached(policy models.LibraryAccessPolicy) {
	s.mu.Lock()
	s.cache[policy.LibraryID] = clonePolicy(policy)
	s.mu.Unlock()
}

func (s *Service) storeItemLibrary(itemID, libraryID string) {
	if itemID == "" || libraryID == "" {
		return
	}
	s.mu.Lock()
	s.items[itemID] = libraryID
	s.mu.Unlock()
}
