package indexer

import (
	"sort"
	"strings"

	"novastream/config"
	"novastream/models"
	"novastream/utils/language"
)

// Playback preferences cascade from global to profile to device, independently
// of the metadata language that selects release search titles.
func (s *Service) getEffectiveAudioLanguage(userID, clientID string, settings config.Settings) string {
	preferred := strings.TrimSpace(settings.Playback.PreferredAudioLanguage)
	if userID != "" && s.userSettings != nil {
		if profile, err := s.userSettings.Get(userID); err == nil && profile != nil && strings.TrimSpace(profile.Playback.PreferredAudioLanguage) != "" {
			preferred = strings.TrimSpace(profile.Playback.PreferredAudioLanguage)
		}
	}
	if clientID != "" && s.clientSettings != nil {
		if client, err := s.clientSettings.Get(clientID, userID); err == nil && client != nil && client.PreferredAudioLanguage != nil {
			preferred = strings.TrimSpace(*client.PreferredAudioLanguage)
		}
	}
	if normalized := language.NormalizeToCode(preferred); normalized != "" {
		return normalized
	}
	return preferred
}

// An explicit profile/device choice precedes every configurable ranking rule.
// Global defaults retain the configurable Language criterion's priority.
func (s *Service) explicitAudioLanguage(userID, clientID string) string {
	preferred := ""
	if userID != "" && s.userSettings != nil {
		if profile, err := s.userSettings.Get(userID); err == nil && profile != nil {
			preferred = strings.TrimSpace(profile.Playback.PreferredAudioLanguage)
		}
	}
	if clientID != "" && s.clientSettings != nil {
		if client, err := s.clientSettings.Get(clientID, userID); err == nil && client != nil && client.PreferredAudioLanguage != nil {
			preferred = strings.TrimSpace(*client.PreferredAudioLanguage)
		}
	}
	return language.NormalizeToCode(preferred)
}

func prioritizeExplicitAudio(results []models.NZBResult, preferred string) {
	if preferred == "" {
		return
	}
	sort.SliceStable(results, func(i, j int) bool { return compareLanguage(results[i], results[j], preferred) < 0 })
}

func prioritizeExplicitAudioScored(results []models.ScoredNZBResult, preferred string) {
	if preferred == "" {
		return
	}
	sort.SliceStable(results, func(i, j int) bool { return compareLanguage(results[i].NZBResult, results[j].NZBResult, preferred) < 0 })
}

// A preference for the title's original language needs no pin: untagged
// releases carry that audio, so pinning would only promote tagged dubs.
func (s *Service) explicitAudioLanguageFor(opts SearchOptions) string {
	preferred := s.explicitAudioLanguage(opts.UserID, opts.ClientID)
	if preferred != "" && language.NormalizeToCode(opts.OriginalLanguage) == preferred {
		return ""
	}
	return preferred
}
