package debrid

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"novastream/models"
)

const (
	defaultTorrentPreflightCandidateLimit = 20
	defaultTorrentPreflightGroupLimit     = 12
	torrentPreflightWorkers               = 4
	torrentPreflightAlternateLimit        = 2
	torrentPreflightTimeout               = 6 * time.Second
	torrentPreflightGroupTimeout          = 4 * time.Second
	torrentMetainfoCacheTTL               = 5 * time.Minute
	torrentMetainfoCacheLimit             = 64
)

type torrentMetainfo struct {
	data      []byte
	filename  string
	expiresAt time.Time
}

type torrentMetainfoCache struct {
	mu      sync.Mutex
	entries map[string]torrentMetainfo
}

func newTorrentMetainfoCache() *torrentMetainfoCache {
	return &torrentMetainfoCache{entries: make(map[string]torrentMetainfo)}
}

func (c *torrentMetainfoCache) put(infoHash string, data []byte, filename string) {
	if c == nil || len(data) == 0 {
		return
	}
	infoHash = strings.ToLower(strings.TrimSpace(infoHash))
	if infoHash == "" {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for hash, entry := range c.entries {
		if !entry.expiresAt.After(now) {
			delete(c.entries, hash)
		}
	}
	if len(c.entries) >= torrentMetainfoCacheLimit {
		var oldestHash string
		var oldestExpiry time.Time
		for hash, entry := range c.entries {
			if oldestHash == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestHash = hash
				oldestExpiry = entry.expiresAt
			}
		}
		delete(c.entries, oldestHash)
	}
	c.entries[infoHash] = torrentMetainfo{
		data:      append([]byte(nil), data...),
		filename:  filename,
		expiresAt: now.Add(torrentMetainfoCacheTTL),
	}
}

func (c *torrentMetainfoCache) get(infoHash string) ([]byte, string, bool) {
	if c == nil {
		return nil, "", false
	}
	infoHash = strings.ToLower(strings.TrimSpace(infoHash))
	if infoHash == "" {
		return nil, "", false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[infoHash]
	if !ok {
		return nil, "", false
	}
	if !entry.expiresAt.After(time.Now()) {
		delete(c.entries, infoHash)
		return nil, "", false
	}
	return append([]byte(nil), entry.data...), entry.filename, true
}

// PrioritizeCachedCandidates safely promotes confirmed-cached debrid candidates
// before serial playback resolution. Torrent-file-only results are grouped by
// release name, downloaded without adding them to a provider, and enriched with
// the v1 info hash from their metainfo so they can join the provider bulk check.
//
// Non-debrid candidates retain their original positions, preserving the user's
// configured service priority.
func (s *PlaybackService) PrioritizeCachedCandidates(ctx context.Context, candidates []models.NZBResult) []models.NZBResult {
	if s == nil || s.healthService == nil || len(candidates) < 2 || !s.supportsTorrentPreflight() {
		return candidates
	}

	preflightCtx, cancel := context.WithTimeout(ctx, torrentPreflightTimeout)
	defer cancel()

	enriched := cloneNZBResults(candidates)
	initialHealth, err := s.CheckQuickCacheOnlyBulk(preflightCtx, enriched)
	if err != nil {
		log.Printf("[debrid-preflight] initial bulk cache check failed; keeping ranked order: %v", err)
		return candidates
	}

	enrichmentLimit := defaultTorrentPreflightCandidateLimit
	if firstCached := firstConfirmedCachedIndex(enriched, initialHealth); firstCached >= 0 {
		enrichmentLimit = firstCached
	}
	if enrichmentLimit > len(enriched) {
		enrichmentLimit = len(enriched)
	}

	enrichedGroups := s.enrichTorrentFileGroups(preflightCtx, enriched, enrichmentLimit, defaultTorrentPreflightGroupLimit)
	health := initialHealth
	if enrichedGroups > 0 {
		health, err = s.CheckQuickCacheOnlyBulk(preflightCtx, enriched)
		if err != nil {
			log.Printf("[debrid-preflight] enriched bulk cache check failed; using initial cache results: %v", err)
			health = initialHealth
		}
	}

	ordered, cachedCount, uncachedCount := reorderDebridCandidatesByCache(enriched, health)
	log.Printf("[debrid-preflight] prioritized candidates: enrichedGroups=%d cached=%d uncached=%d total=%d",
		enrichedGroups, cachedCount, uncachedCount, len(candidates))
	return ordered
}

func (s *PlaybackService) supportsTorrentPreflight() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	settings, err := s.cfg.Load()
	if err != nil {
		return false
	}
	var torboxProviderIndex = -1
	for i := range settings.Streaming.DebridProviders {
		p := settings.Streaming.DebridProviders[i]
		if p.Enabled && strings.TrimSpace(p.APIKey) != "" && strings.EqualFold(p.Provider, "torbox") {
			torboxProviderIndex = i
			break
		}
	}
	if torboxProviderIndex < 0 {
		return false
	}
	provider := &settings.Streaming.DebridProviders[torboxProviderIndex]
	return shouldUseQuickTorboxCacheCheck(settings.Streaming.DebridProviders, provider, "", "bulk")
}

func cloneNZBResults(candidates []models.NZBResult) []models.NZBResult {
	out := append([]models.NZBResult(nil), candidates...)
	for i := range out {
		if candidates[i].Attributes == nil {
			continue
		}
		out[i].Attributes = make(map[string]string, len(candidates[i].Attributes)+1)
		for key, value := range candidates[i].Attributes {
			out[i].Attributes[key] = value
		}
	}
	return out
}

func firstConfirmedCachedIndex(candidates []models.NZBResult, health []*DebridHealthCheck) int {
	for i := range candidates {
		if candidates[i].ServiceType != models.ServiceTypeDebrid || i >= len(health) || health[i] == nil {
			continue
		}
		if health[i].Cached {
			return i
		}
	}
	return -1
}

type torrentPreflightGroup struct {
	key     string
	indexes []int
	urls    []string
}

type torrentPreflightResult struct {
	group    *torrentPreflightGroup
	hash     string
	data     []byte
	filename string
	err      error
}

func (s *PlaybackService) enrichTorrentFileGroups(ctx context.Context, candidates []models.NZBResult, candidateLimit, groupLimit int) int {
	if candidateLimit <= 0 || groupLimit <= 0 {
		return 0
	}
	if candidateLimit > len(candidates) {
		candidateLimit = len(candidates)
	}

	groupsByKey := make(map[string]*torrentPreflightGroup)
	groups := make([]*torrentPreflightGroup, 0)
	for i := 0; i < candidateLimit; i++ {
		candidate := candidates[i]
		if !torrentFileCandidateNeedsHash(candidate) {
			continue
		}
		key := normalizedReleaseGroupKey(candidate.Title)
		if key == "" {
			key = fmt.Sprintf("candidate:%d", i)
		}
		group := groupsByKey[key]
		if group == nil {
			if len(groups) >= groupLimit {
				continue
			}
			group = &torrentPreflightGroup{key: key}
			groupsByKey[key] = group
			groups = append(groups, group)
		}
		group.indexes = append(group.indexes, i)
		torrentURL := strings.TrimSpace(candidate.Attributes["torrentURL"])
		if torrentURL == "" {
			torrentURL = strings.TrimSpace(candidate.DownloadURL)
		}
		if torrentURL == "" {
			torrentURL = strings.TrimSpace(candidate.Link)
		}
		if torrentURL != "" && !containsString(group.urls, torrentURL) {
			group.urls = append(group.urls, torrentURL)
		}
	}
	if len(groups) == 0 {
		return 0
	}

	workerCount := torrentPreflightWorkers
	if len(groups) < workerCount {
		workerCount = len(groups)
	}
	queue := make(chan *torrentPreflightGroup)
	results := make(chan torrentPreflightResult, len(groups))
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for group := range queue {
				hash, data, filename, err := s.resolveTorrentGroupInfoHash(ctx, group)
				results <- torrentPreflightResult{
					group: group, hash: hash, data: data, filename: filename, err: err,
				}
			}
		}()
	}
	go func() {
		defer close(queue)
		for _, group := range groups {
			select {
			case <-ctx.Done():
				return
			case queue <- group:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	enrichedCount := 0
	for result := range results {
		if result.err != nil {
			log.Printf("[debrid-preflight] torrent metadata lookup failed for release=%q: %v", result.group.key, result.err)
			continue
		}
		for _, index := range result.group.indexes {
			if candidates[index].Attributes == nil {
				candidates[index].Attributes = make(map[string]string)
			}
			candidates[index].Attributes["infoHash"] = result.hash
		}
		s.preflightData.put(result.hash, result.data, result.filename)
		enrichedCount++
		log.Printf("[debrid-preflight] enriched release=%q candidates=%d hash=%s",
			result.group.key, len(result.group.indexes), result.hash)
	}
	return enrichedCount
}

func torrentFileCandidateNeedsHash(candidate models.NZBResult) bool {
	if candidate.ServiceType != models.ServiceTypeDebrid || candidate.Attributes["preresolved"] == "true" {
		return false
	}
	if quickCacheInfoHash(candidate) != "" {
		return false
	}
	torrentURL := strings.TrimSpace(candidate.Attributes["torrentURL"])
	if torrentURL == "" {
		torrentURL = strings.TrimSpace(candidate.DownloadURL)
	}
	if torrentURL == "" {
		torrentURL = strings.TrimSpace(candidate.Link)
	}
	return strings.HasPrefix(torrentURL, "http://") || strings.HasPrefix(torrentURL, "https://")
}

func (s *PlaybackService) resolveTorrentGroupInfoHash(ctx context.Context, group *torrentPreflightGroup) (string, []byte, string, error) {
	if group == nil || len(group.urls) == 0 {
		return "", nil, "", fmt.Errorf("no torrent URLs")
	}
	limit := len(group.urls)
	if limit > torrentPreflightAlternateLimit {
		limit = torrentPreflightAlternateLimit
	}

	groupCtx, cancel := context.WithTimeout(ctx, torrentPreflightGroupTimeout)
	defer cancel()
	type result struct {
		hash     string
		data     []byte
		filename string
		err      error
	}
	resultCh := make(chan result, limit)
	for _, torrentURL := range group.urls[:limit] {
		go func(url string) {
			data, filename, err := s.downloadTorrentFile(groupCtx, url)
			if err != nil {
				resultCh <- result{err: err}
				return
			}
			hash, err := torrentV1InfoHash(data)
			resultCh <- result{hash: hash, data: data, filename: filename, err: err}
		}(torrentURL)
	}

	var lastErr error
	for i := 0; i < limit; i++ {
		select {
		case <-ctx.Done():
			return "", nil, "", ctx.Err()
		case result := <-resultCh:
			if result.err == nil && result.hash != "" {
				cancel()
				return result.hash, result.data, result.filename, nil
			}
			if result.err != nil {
				lastErr = result.err
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("torrent metadata did not contain a v1 info hash")
	}
	return "", nil, "", lastErr
}

func (s *PlaybackService) torrentFileForResolution(ctx context.Context, infoHash, torrentURL string) ([]byte, string, bool, error) {
	if data, filename, ok := s.preflightData.get(infoHash); ok {
		return data, filename, true, nil
	}
	log.Printf("[debrid-playback] downloading torrent file from %s", safeURLForLog(torrentURL))
	data, filename, err := s.downloadTorrentFile(ctx, torrentURL)
	return data, filename, false, err
}

func reorderDebridCandidatesByCache(candidates []models.NZBResult, health []*DebridHealthCheck) ([]models.NZBResult, int, int) {
	type rankedCandidate struct {
		result models.NZBResult
		state  int
	}
	const (
		cacheStateCached = iota
		cacheStateUnknown
		cacheStateUncached
	)

	debridPositions := make([]int, 0)
	debrid := make([]rankedCandidate, 0)
	cachedCount := 0
	uncachedCount := 0
	for i, candidate := range candidates {
		if candidate.ServiceType != models.ServiceTypeDebrid {
			continue
		}
		state := cacheStateUnknown
		if i < len(health) && health[i] != nil {
			switch {
			case health[i].Cached:
				state = cacheStateCached
				cachedCount++
			case health[i].Status == "not_cached":
				state = cacheStateUncached
				uncachedCount++
			}
		}
		debridPositions = append(debridPositions, i)
		debrid = append(debrid, rankedCandidate{result: candidate, state: state})
	}
	if cachedCount == 0 {
		return candidates, 0, uncachedCount
	}

	orderedDebrid := make([]models.NZBResult, 0, len(debrid))
	for _, targetState := range []int{cacheStateCached, cacheStateUnknown, cacheStateUncached} {
		for _, candidate := range debrid {
			if candidate.state == targetState {
				orderedDebrid = append(orderedDebrid, candidate.result)
			}
		}
	}
	out := append([]models.NZBResult(nil), candidates...)
	for i, position := range debridPositions {
		out[position] = orderedDebrid[i]
	}
	return out, cachedCount, uncachedCount
}

func normalizedReleaseGroupKey(title string) string {
	var builder strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			space = false
			continue
		}
		if builder.Len() > 0 && !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func torrentV1InfoHash(data []byte) (string, error) {
	if len(data) < 2 || data[0] != 'd' {
		return "", fmt.Errorf("torrent metainfo must be a bencoded dictionary")
	}
	position := 1
	for position < len(data) && data[position] != 'e' {
		key, next, err := readBencodedString(data, position)
		if err != nil {
			return "", fmt.Errorf("read torrent dictionary key: %w", err)
		}
		position = next
		valueStart := position
		valueEnd, err := skipBencodedValue(data, position, 0)
		if err != nil {
			return "", fmt.Errorf("read torrent dictionary value %q: %w", string(key), err)
		}
		if string(key) == "info" {
			sum := sha1.Sum(data[valueStart:valueEnd])
			return hex.EncodeToString(sum[:]), nil
		}
		position = valueEnd
	}
	return "", fmt.Errorf("torrent metainfo is missing info dictionary")
}

func readBencodedString(data []byte, position int) ([]byte, int, error) {
	if position >= len(data) || data[position] < '0' || data[position] > '9' {
		return nil, position, fmt.Errorf("expected string length at byte %d", position)
	}
	length := 0
	for position < len(data) && data[position] >= '0' && data[position] <= '9' {
		length = length*10 + int(data[position]-'0')
		position++
		if length > len(data) {
			return nil, position, fmt.Errorf("string length exceeds input")
		}
	}
	if position >= len(data) || data[position] != ':' {
		return nil, position, fmt.Errorf("expected string separator")
	}
	position++
	end := position + length
	if end < position || end > len(data) {
		return nil, position, fmt.Errorf("string exceeds input")
	}
	return data[position:end], end, nil
}

func skipBencodedValue(data []byte, position, depth int) (int, error) {
	if depth > 128 {
		return position, fmt.Errorf("bencode nesting exceeds limit")
	}
	if position >= len(data) {
		return position, fmt.Errorf("unexpected end of input")
	}
	switch data[position] {
	case 'i':
		end := position + 1
		for end < len(data) && data[end] != 'e' {
			end++
		}
		if end >= len(data) {
			return position, fmt.Errorf("unterminated integer")
		}
		return end + 1, nil
	case 'l':
		position++
		for position < len(data) && data[position] != 'e' {
			next, err := skipBencodedValue(data, position, depth+1)
			if err != nil {
				return position, err
			}
			position = next
		}
		if position >= len(data) {
			return position, fmt.Errorf("unterminated list")
		}
		return position + 1, nil
	case 'd':
		position++
		for position < len(data) && data[position] != 'e' {
			_, next, err := readBencodedString(data, position)
			if err != nil {
				return position, err
			}
			position = next
			position, err = skipBencodedValue(data, position, depth+1)
			if err != nil {
				return position, err
			}
		}
		if position >= len(data) {
			return position, fmt.Errorf("unterminated dictionary")
		}
		return position + 1, nil
	default:
		if data[position] >= '0' && data[position] <= '9' {
			_, next, err := readBencodedString(data, position)
			return next, err
		}
		return position, fmt.Errorf("unexpected token %q", data[position])
	}
}
