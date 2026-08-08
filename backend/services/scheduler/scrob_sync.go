package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"novastream/config"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/scrob"
)

// executeScrobHistorySync synchronizes completed watch state with a self-hosted
// Scrob user. Scrob exposes API-key reads, while writes currently require a JWT
// obtained from its password login endpoint.
func (s *Service) executeScrobHistorySync(task config.ScheduledTask) (SyncResult, error) {
	s.mu.RLock()
	historySvc := s.historyService
	client := s.scrobClient
	s.mu.RUnlock()
	if historySvc == nil {
		return SyncResult{}, errors.New("history service not configured")
	}
	if client == nil {
		return SyncResult{}, errors.New("Scrob client not configured")
	}

	profileID, err := s.resolveTaskProfileID(task)
	if err != nil {
		return SyncResult{}, err
	}
	accountID := strings.TrimSpace(task.Config["scrobAccountId"])
	if accountID == "" || profileID == "" {
		return SyncResult{}, errors.New("missing scrobAccountId or profileId in task config")
	}

	settings, err := s.configManager.Load()
	if err != nil {
		return SyncResult{}, fmt.Errorf("load settings: %w", err)
	}
	account := settings.Scrob.GetAccountByID(accountID)
	if account == nil {
		return SyncResult{}, errors.New("Scrob account not found")
	}
	if strings.TrimSpace(account.BaseURL) == "" || strings.TrimSpace(account.APIKey) == "" {
		return SyncResult{}, errors.New("Scrob account requires a base URL and API key")
	}

	direction := task.Config["syncDirection"]
	if direction == "" {
		direction = "scrob_to_local"
	}
	dryRun := task.Config["dryRun"] == "true"
	switch direction {
	case "scrob_to_local":
		return s.syncScrobHistoryToLocal(account, profileID, dryRun)
	case "local_to_scrob":
		return s.syncLocalHistoryToScrob(task, account, profileID, dryRun)
	case "bidirectional":
		in, err := s.syncScrobHistoryToLocal(account, profileID, dryRun)
		if err != nil {
			return in, err
		}
		out, err := s.syncLocalHistoryToScrob(task, account, profileID, dryRun)
		if err != nil {
			return out, err
		}
		return SyncResult{Count: in.Count + out.Count, DryRun: dryRun, ToAdd: append(in.ToAdd, out.ToAdd...), ToRemove: append(in.ToRemove, out.ToRemove...)}, nil
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", direction)
	}
}

func (s *Service) syncScrobHistoryToLocal(account *config.ScrobAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}
	s.mu.RLock()
	client, historySvc := s.scrobClient, s.historyService
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	events, err := client.GetHistory(ctx, account.BaseURL, account.APIKey)
	if err != nil {
		return result, fmt.Errorf("fetch Scrob history: %w", err)
	}

	watched := true
	seen := make(map[string]struct{})
	updates := make([]models.WatchHistoryUpdate, 0, len(events))
	for _, event := range events {
		update := scrobEventToUpdate(event, &watched)
		if update == nil {
			continue
		}
		key := strings.ToLower(update.MediaType + ":" + update.ItemID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if dryRun {
			result.ToAdd = append(result.ToAdd, config.DryRunItem{Name: update.Name, MediaType: update.MediaType, ID: update.ItemID})
		} else {
			updates = append(updates, *update)
		}
	}
	if dryRun {
		result.Count = len(result.ToAdd)
		return result, nil
	}
	if len(updates) > 0 {
		result.Count, err = historySvc.ImportWatchHistory(profileID, updates)
		if err != nil {
			return result, fmt.Errorf("import Scrob watch history: %w", err)
		}
	}
	log.Printf("[scheduler] Imported %d/%d unique items from Scrob history", result.Count, len(updates))
	return result, nil
}

func scrobEventToUpdate(event scrob.HistoryEvent, watched *bool) *models.WatchHistoryUpdate {
	if !event.Completed {
		return nil
	}
	m := event.Media
	var watchedAt time.Time
	if event.WatchedAt != nil {
		watchedAt = event.WatchedAt.UTC()
	}
	switch m.Type {
	case "movie":
		if m.TMDBID <= 0 {
			return nil
		}
		year := 0
		if len(m.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(m.ReleaseDate[:4])
		}
		id := strconv.Itoa(m.TMDBID)
		return &models.WatchHistoryUpdate{MediaType: "movie", ItemID: "tmdb:" + id, Name: m.Title, Year: year, Watched: watched, WatchedAt: watchedAt, ExternalIDs: map[string]string{"tmdb": id}}
	case "episode":
		if m.ShowTMDBID <= 0 || m.SeasonNumber < 0 || m.EpisodeNumber <= 0 {
			return nil
		}
		showID := strconv.Itoa(m.ShowTMDBID)
		seriesID := "tmdb:tv:" + showID
		ext := map[string]string{"tmdb": showID}
		if m.TMDBID > 0 {
			ext["episodeTmdb"] = strconv.Itoa(m.TMDBID)
		}
		if m.ShowTVDBID > 0 {
			ext["tvdb"] = strconv.Itoa(m.ShowTVDBID)
		}
		return &models.WatchHistoryUpdate{
			MediaType: "episode", ItemID: fmt.Sprintf("%s:s%02de%02d", seriesID, m.SeasonNumber, m.EpisodeNumber), Name: m.Title,
			Watched: watched, WatchedAt: watchedAt, ExternalIDs: ext, SeasonNumber: m.SeasonNumber, EpisodeNumber: m.EpisodeNumber,
			SeriesID: seriesID, SeriesName: m.ShowTitle,
		}
	}
	return nil
}

func (s *Service) syncLocalHistoryToScrob(task config.ScheduledTask, account *config.ScrobAccount, profileID string, dryRun bool) (SyncResult, error) {
	result := SyncResult{DryRun: dryRun}
	s.mu.RLock()
	client, historySvc := s.scrobClient, s.historyService
	s.mu.RUnlock()
	items, err := historySvc.ListWatchHistory(profileID)
	if err != nil {
		return result, fmt.Errorf("list local history: %w", err)
	}
	// Initial exports may contain thousands of plays and Scrob currently accepts
	// them one at a time. Keep enough headroom for a full backfill; later runs
	// deduplicate against remote history and are substantially shorter.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	remote, err := client.GetHistory(ctx, account.BaseURL, account.APIKey)
	if err != nil {
		return result, fmt.Errorf("fetch Scrob history for deduplication: %w", err)
	}
	remoteByKey := make(map[string]int)
	for _, event := range remote {
		if key := scrobRemoteKey(event.Media); key != "" {
			remoteByKey[key] = event.Media.TMDBID
		}
	}

	exportKey := task.ID + ":scrob_export"
	s.lastFullSyncTimesMu.Lock()
	lastFull, haveFull := s.lastFullSyncTimes[exportKey]
	s.lastFullSyncTimesMu.Unlock()
	isFull := task.Config["fullExport"] == "true" || !haveFull || time.Since(lastFull) >= 6*time.Hour
	var since time.Time
	if !isFull && task.LastRunAt != nil {
		since = task.LastRunAt.Add(-5 * time.Minute)
	}
	type outbound struct {
		item         models.WatchHistoryItem
		event        scrob.WatchEvent
		key          string
		removeTMDBID int
	}
	var changes []outbound
	for _, item := range items {
		if !since.IsZero() && item.Watched && item.WatchedAt.Before(since) {
			continue
		}
		if !since.IsZero() && !item.Watched && item.UpdatedAt.Before(since) {
			continue
		}
		event, key, ok := localItemToScrob(item)
		if !ok {
			continue
		}
		remoteTMDBID, exists := remoteByKey[key]
		if item.Watched == exists {
			continue
		}
		changes = append(changes, outbound{item: item, event: event, key: key, removeTMDBID: remoteTMDBID})
	}

	if dryRun {
		for _, change := range changes {
			d := config.DryRunItem{Name: change.item.Name, MediaType: change.item.MediaType, ID: change.item.ItemID}
			if change.item.Watched {
				result.ToAdd = append(result.ToAdd, d)
			} else {
				result.ToRemove = append(result.ToRemove, d)
			}
		}
		result.Count = len(result.ToAdd) + len(result.ToRemove)
		return result, nil
	}
	if len(changes) == 0 {
		if isFull {
			s.lastFullSyncTimesMu.Lock()
			s.lastFullSyncTimes[exportKey] = time.Now().UTC()
			s.lastFullSyncTimesMu.Unlock()
		}
		return result, nil
	}
	twoFactorCode := ""
	if strings.TrimSpace(account.TOTPSecret) != "" {
		twoFactorCode, err = scrob.GenerateTOTPCode(account.TOTPSecret, time.Now().UTC())
		if err != nil {
			return result, err
		}
	}
	token, err := client.Login(ctx, account.BaseURL, account.APIKey, account.Username, account.Password, twoFactorCode)
	if err != nil {
		return result, err
	}
	failed := 0
	var firstFailure error
	for _, change := range changes {
		if change.item.Watched {
			err = client.AddHistory(ctx, account.BaseURL, account.APIKey, token, change.event)
		} else if change.removeTMDBID > 0 {
			err = client.RemoveHistory(ctx, account.BaseURL, account.APIKey, token, change.removeTMDBID, change.event.MediaType)
		} else {
			continue
		}
		if err != nil {
			failure := fmt.Errorf("sync %s %q to Scrob: %w", change.item.MediaType, change.item.Name, err)
			if firstFailure == nil {
				firstFailure = failure
			}
			failed++
			log.Printf("[scheduler] Scrob export skipped item after error: %v", failure)
			if ctx.Err() != nil {
				return result, fmt.Errorf("Scrob export stopped after syncing %d of %d changes: %w", result.Count, len(changes), ctx.Err())
			}
			continue
		}
		result.Count++
	}
	if failed > 0 {
		log.Printf("[scheduler] Scrob export completed partially: synced=%d failed=%d total=%d", result.Count, failed, len(changes))
		return result, fmt.Errorf("Scrob export synced %d of %d changes; %d failed (first error: %w)", result.Count, len(changes), failed, firstFailure)
	}
	if isFull {
		s.lastFullSyncTimesMu.Lock()
		s.lastFullSyncTimes[exportKey] = time.Now().UTC()
		s.lastFullSyncTimesMu.Unlock()
	}
	return result, nil
}

func scrobRemoteKey(media scrob.Media) string {
	switch media.Type {
	case "movie":
		if media.TMDBID > 0 {
			return fmt.Sprintf("movie:%d", media.TMDBID)
		}
	case "episode":
		if media.ShowTMDBID > 0 && media.EpisodeNumber > 0 {
			return fmt.Sprintf("episode:%d:%d:%d", media.ShowTMDBID, media.SeasonNumber, media.EpisodeNumber)
		}
	}
	return ""
}

func localItemToScrob(item models.WatchHistoryItem) (scrob.WatchEvent, string, bool) {
	event := scrob.WatchEvent{MediaType: item.MediaType, Completed: true}
	if !item.WatchedAt.IsZero() {
		watchedAt := item.WatchedAt.UTC()
		event.WatchedAt = &watchedAt
	}
	switch item.MediaType {
	case "movie":
		id := scrobPositiveID(item.ExternalIDs["tmdb"])
		if id == 0 {
			id = scrobIDFromItem(item.ItemID, "tmdb:")
		}
		if id == 0 {
			return event, "", false
		}
		event.TMDBID = id
		return event, fmt.Sprintf("movie:%d", id), true
	case "episode":
		ext := mediaidentity.EnrichShowExternalIDs(item.SeriesID, item.ItemID, item.ExternalIDs)
		showID := scrobPositiveID(ext["tmdb"])
		if showID == 0 || item.SeasonNumber < 0 || item.EpisodeNumber <= 0 {
			return event, "", false
		}
		event.TMDBID = scrobPositiveID(ext["episodeTmdb"])
		event.SeriesTMDBID = showID
		event.SeriesTVDBID = scrobPositiveID(ext["tvdb"])
		event.SeasonNumber, event.EpisodeNumber = item.SeasonNumber, item.EpisodeNumber
		return event, fmt.Sprintf("episode:%d:%d:%d", showID, item.SeasonNumber, item.EpisodeNumber), true
	}
	return event, "", false
}

func scrobPositiveID(raw string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(raw))
	if n > 0 {
		return n
	}
	return 0
}
func scrobIDFromItem(itemID, prefix string) int {
	if strings.HasPrefix(strings.ToLower(itemID), prefix) {
		return scrobPositiveID(itemID[len(prefix):])
	}
	return 0
}
