package epg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

const hdHomeRunGuideURL = "https://api.hdhomerun.com/api/xmltv"

type hdHomeRunGuideCache struct {
	NextRefresh time.Time           `json:"nextRefresh"`
	Schedule    *models.EPGSchedule `json:"schedule,omitempty"`
	LastError   string              `json:"lastError,omitempty"`
}

func hdHomeRunSources(settings config.Settings) []config.LivePlaylistSource {
	sources := configuredLiveSources(settings)
	if len(sources) == 0 {
		sources = []config.LivePlaylistSource{{Mode: settings.Live.Mode, HDHomeRunHost: settings.Live.HDHomeRunHost, HDHomeRunGuideEmail: settings.Live.HDHomeRunGuideEmail, HDHomeRunGuideDeviceIDs: settings.Live.HDHomeRunGuideDeviceIDs, EPG: settings.Live.EPG}}
	}
	var result []config.LivePlaylistSource
	seen := map[string]bool{}
	for _, source := range sources {
		if !liveSourceEnabled(source) || !strings.EqualFold(strings.TrimSpace(source.Mode), "hdhomerun") ||
			(!settings.Live.EPG.Enabled && !source.EPG.Enabled) {
			continue
		}
		// A source-specific XMLTV override replaces the built-in guide.
		if len(appendEPGXMLTVSources(nil, source.EPG, "", "", 0)) > 0 {
			continue
		}
		key := hdHomeRunSourceKey(source)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, source)
	}
	return result
}

// SetProfileHDHomeRunSources includes explicit profile tuner overrides in the
// shared guide cache. The provider is called without holding service locks.
func (s *Service) SetProfileHDHomeRunSources(provider func(config.Settings) []config.LivePlaylistSource) {
	s.hdHomeRunMu.Lock()
	s.hdHomeRunProfileSources = provider
	s.hdHomeRunMu.Unlock()
}

func (s *Service) hdHomeRunSources(settings config.Settings) []config.LivePlaylistSource {
	result := hdHomeRunSources(settings)
	s.hdHomeRunMu.Lock()
	provider := s.hdHomeRunProfileSources
	s.hdHomeRunMu.Unlock()
	if provider != nil {
		extra := settings
		extra.Live.EPG.Enabled = false
		extra.Live.Sources = provider(settings)
		extra.Live.PlaylistSources = nil
		if len(extra.Live.Sources) > 0 {
			result = append(result, hdHomeRunSources(extra)...)
		}
	}
	seen := map[string]bool{}
	unique := result[:0]
	for _, source := range result {
		key := hdHomeRunSourceKey(source)
		if !seen[key] {
			unique = append(unique, source)
			seen[key] = true
		}
	}
	return unique
}

func (s *Service) HasHDHomeRunGuideForSettings(settings config.Settings) bool {
	return len(s.hdHomeRunSources(settings)) > 0
}

func (s *Service) HasHDHomeRunGuide() bool {
	settings, err := s.cfgManager.Load()
	return err == nil && len(s.hdHomeRunSources(settings)) > 0
}

// HDHomeRunRefreshDue lets the scheduler wake at the tuner's randomized deadline,
// independently of the ordinary XMLTV task frequency. Refresh still reuses cached
// tuner data when invoked by another guide source or manually before that deadline.
func (s *Service) HDHomeRunRefreshDue() bool {
	settings, err := s.cfgManager.Load()
	if err != nil {
		return false
	}
	sources := s.hdHomeRunSources(settings)
	s.hdHomeRunMu.Lock()
	defer s.hdHomeRunMu.Unlock()
	for _, source := range sources {
		_, err := config.HDHomeRunURL(source.HDHomeRunHost, "/discover.json")
		if err != nil {
			continue
		}
		cache, ok := s.hdHomeRunCache[hdHomeRunSourceKey(source)]
		if !ok || !time.Now().Before(cache.NextRefresh) {
			return true
		}
	}
	return false
}

// Authentication changes must not reuse a previous guide or error deadline.
// Keep the legacy key for automatic DeviceAuth, and hash account identifiers so
// neither in-memory keys nor cache filenames reveal the email or device IDs.
func hdHomeRunSourceKey(source config.LivePlaylistSource) string {
	key, _ := config.HDHomeRunURL(source.HDHomeRunHost, "/discover.json")
	if key == "" {
		key = source.HDHomeRunHost
	}
	email, ids, err := config.NormalizeHDHomeRunGuideAuth(source.HDHomeRunGuideEmail, source.HDHomeRunGuideDeviceIDs)
	if err != nil {
		email, ids = strings.TrimSpace(source.HDHomeRunGuideEmail), strings.TrimSpace(source.HDHomeRunGuideDeviceIDs)
	}
	if email != "" || ids != "" {
		hash := sha256.Sum256([]byte(email + "\x00" + ids))
		key += "#guide=" + hex.EncodeToString(hash[:16])
	}
	return key
}

func (s *Service) hdHomeRunCachePath(key string) string {
	hash := sha256.Sum256([]byte(key))
	return filepath.Join(s.storageDir, "epg", "hdhomerun-"+hex.EncodeToString(hash[:16])+".json")
}

func (s *Service) fetchHDHomeRunEPG(ctx context.Context, source config.LivePlaylistSource, target *models.EPGSchedule) error {
	discoveryURL, err := config.HDHomeRunURL(source.HDHomeRunHost, "/discover.json")
	if err != nil {
		return err
	}
	key := hdHomeRunSourceKey(source)
	s.hdHomeRunMu.Lock()
	if s.hdHomeRunCache == nil {
		s.hdHomeRunCache = make(map[string]hdHomeRunGuideCache)
	}
	cache, loaded := s.hdHomeRunCache[key]
	s.hdHomeRunMu.Unlock()
	if !loaded {
		if data, err := os.ReadFile(s.hdHomeRunCachePath(key)); err == nil {
			_ = json.Unmarshal(data, &cache)
		}
	}
	if !time.Now().Before(cache.NextRefresh) || (countSchedulePrograms(cache.Schedule) == 0 && cache.LastError == "") {
		started := time.Now()
		log.Printf("[hdhomerun-epg] refresh start source=%q tuner=%q cachedPrograms=%d previousError=%v", source.Name, key, countSchedulePrograms(cache.Schedule), cache.LastError != "")
		guide := &models.EPGSchedule{Channels: make(map[string]models.EPGChannel), Programs: make(map[string][]models.EPGProgram)}
		err = s.downloadHDHomeRunGuide(ctx, discoveryURL, guide, source)
		if err == nil && countSchedulePrograms(guide) == 0 {
			err = errors.New("HDHomeRun guide returned no usable programs")
		}
		if err == nil {
			guide.LastUpdated = time.Now().UTC()
			cache.Schedule = guide
			cache.LastError = ""
			cache.NextRefresh = time.Now().UTC().Add(20*time.Hour + time.Duration(rand.Int64N(int64(8*time.Hour))))
		} else {
			// Bound retries while retaining the last working tuner guide, even when
			// other sources refresh successfully. Errors never contain DeviceAuth.
			cache.LastError = err.Error()
			cache.NextRefresh = time.Now().UTC().Add(time.Hour)
		}
		log.Printf("[hdhomerun-epg] refresh result source=%q tuner=%q success=%v retainedCache=%v channels=%d programs=%d nextRefresh=%s elapsed=%s error=%q",
			source.Name, key, err == nil, err != nil && cache.Schedule != nil, hdHomeRunChannelCount(cache.Schedule), countSchedulePrograms(cache.Schedule), cache.NextRefresh.Format(time.RFC3339), time.Since(started).Round(time.Millisecond), cache.LastError)
		if data, marshalErr := json.Marshal(cache); marshalErr == nil {
			path := s.hdHomeRunCachePath(key)
			if writeErr := os.WriteFile(path+".tmp", data, 0600); writeErr == nil {
				if renameErr := os.Rename(path+".tmp", path); renameErr != nil {
					log.Print("[epg] failed to persist HDHomeRun guide cache")
				}
			} else {
				log.Print("[epg] failed to persist HDHomeRun guide cache")
			}
		}
	} else {
		log.Printf("[hdhomerun-epg] cache reuse source=%q tuner=%q channels=%d programs=%d lastUpdated=%s nextRefresh=%s lastError=%q",
			source.Name, key, hdHomeRunChannelCount(cache.Schedule), countSchedulePrograms(cache.Schedule), hdHomeRunGuideUpdated(cache.Schedule), cache.NextRefresh.Format(time.RFC3339), cache.LastError)
		logHDHomeRunGuideCoverage(key, "cache", cache.Schedule, nil, time.Now().UTC())
	}
	s.hdHomeRunMu.Lock()
	s.hdHomeRunCache[key] = cache
	s.hdHomeRunMu.Unlock()
	if cache.Schedule != nil {
		addedPrograms, skippedProgramChannels := 0, 0
		for id, channel := range cache.Schedule.Channels {
			target.Channels[id] = channel
		}
		for id, programs := range cache.Schedule.Programs {
			// Tuners in the same market commonly share station IDs. Import the
			// first guide once rather than duplicate every program for each tuner.
			if len(target.Programs[id]) == 0 {
				target.Programs[id] = append([]models.EPGProgram(nil), programs...)
				addedPrograms += len(programs)
			} else {
				skippedProgramChannels++
			}
		}
		target.SourceType = "xmltv"
		log.Printf("[hdhomerun-epg] merge source=%q tuner=%q addedPrograms=%d skippedExistingProgramChannels=%d", source.Name, key, addedPrograms, skippedProgramChannels)
	}
	if cache.LastError != "" {
		return errors.New(cache.LastError)
	}
	return nil
}
