package debrid

import (
	"context"
	"sort"
	"strings"
	"sync"

	"novastream/config"
	"novastream/models"
)

type providerAvailabilityState struct {
	known  bool
	cached bool
}

func providerAwareResultsEnabled(settings config.Settings) bool {
	for _, source := range settings.Filtering.SourcePriority {
		if strings.HasPrefix(normalizeResultSource(source), "debrid:") {
			return true
		}
	}
	for _, provider := range settings.Streaming.DebridProviders {
		if provider.Enabled && provider.Filtering != nil {
			return true
		}
	}
	return false
}

func normalizeResultSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.HasPrefix(source, "debrid:") {
		return "debrid:" + canonicalDebridProvider(strings.TrimPrefix(source, "debrid:"))
	}
	return source
}

func canonicalDebridProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	compact := strings.NewReplacer("-", "", "_", "", " ", "", "+", "").Replace(provider)
	switch compact {
	case "rd", "realdebrid", "realdebridcom":
		return "realdebrid"
	case "tb", "torbox", "torboxapp":
		return "torbox"
	case "ad", "alldebrid", "alldebridcom":
		return "alldebrid"
	case "pm", "premiumize", "premiumizeme":
		return "premiumize"
	default:
		return compact
	}
}

func configuredProviderKey(provider config.DebridProviderSettings) string {
	return canonicalDebridProvider(provider.Provider)
}

func resultProviderKey(result models.NZBResult) string {
	for _, key := range []string{"provider", "debridProvider", "resolverProvider"} {
		if value := canonicalDebridProvider(result.Attributes[key]); value != "" {
			return value
		}
	}
	return ""
}

func identifyPreResolvedProvider(result models.NZBResult, providers []config.DebridProviderSettings) string {
	if provider := resultProviderKey(result); provider != "" {
		return provider
	}
	if result.Attributes["preresolved"] != "true" {
		return ""
	}
	tracker := canonicalDebridProvider(result.Attributes["tracker"])
	for _, provider := range providers {
		key := configuredProviderKey(provider)
		name := canonicalDebridProvider(provider.Name)
		if tracker != "" && (tracker == key || tracker == name) {
			return key
		}
	}
	return ""
}

func providerInfoHash(result models.NZBResult) string {
	hash := strings.ToLower(strings.TrimSpace(result.Attributes["infoHash"]))
	if hash == "" && strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
		hash = extractInfoHashFromMagnet(result.Link)
	}
	return hash
}

func cloneResultForProvider(result models.NZBResult, provider string) models.NZBResult {
	clone := result
	clone.Attributes = make(map[string]string, len(result.Attributes)+3)
	for key, value := range result.Attributes {
		clone.Attributes[key] = value
	}
	clone.Attributes["provider"] = provider
	clone.Attributes["debridProvider"] = provider
	clone.Attributes["providerAvailability"] = "cached"
	if clone.GUID != "" {
		clone.GUID += "#debrid-provider=" + provider
	}
	return clone
}

// expandResultsByProviderAvailability turns a generic torrent result into one
// result per provider where it is cached. Unknown checks retain the generic
// result so a provider outage cannot make playable content disappear.
func expandResultsByProviderAvailability(
	results []models.NZBResult,
	providers []config.DebridProviderSettings,
	availability map[string]map[string]providerAvailabilityState,
) []models.NZBResult {
	out := make([]models.NZBResult, 0, len(results))
	for _, result := range results {
		if provider := identifyPreResolvedProvider(result, providers); provider != "" {
			out = append(out, cloneResultForProvider(result, provider))
			continue
		}
		if result.Attributes["preresolved"] == "true" {
			out = append(out, result)
			continue
		}
		hash := providerInfoHash(result)
		if hash == "" {
			out = append(out, result)
			continue
		}

		matched := false
		unknown := false
		for _, provider := range providers {
			if !provider.Enabled || strings.TrimSpace(provider.APIKey) == "" {
				continue
			}
			key := configuredProviderKey(provider)
			state := availability[key][hash]
			if !state.known {
				unknown = true
				continue
			}
			if state.cached {
				out = append(out, cloneResultForProvider(result, key))
				matched = true
			}
		}
		if !matched && unknown {
			out = append(out, result)
		}
	}
	return out
}

func (s *SearchService) expandProviderAwareResults(ctx context.Context, settings config.Settings, results []models.NZBResult) []models.NZBResult {
	if !providerAwareResultsEnabled(settings) || len(results) == 0 {
		return results
	}

	hashSet := make(map[string]struct{})
	for _, result := range results {
		if result.Attributes["preresolved"] == "true" {
			continue
		}
		if hash := providerInfoHash(result); hash != "" {
			hashSet[hash] = struct{}{}
		}
	}
	hashes := make([]string, 0, len(hashSet))
	for hash := range hashSet {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)

	availability := make(map[string]map[string]providerAvailabilityState)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, providerConfig := range settings.Streaming.DebridProviders {
		providerConfig := providerConfig
		if !providerConfig.Enabled || strings.TrimSpace(providerConfig.APIKey) == "" {
			continue
		}
		key := configuredProviderKey(providerConfig)
		client, ok := GetProvider(key, providerConfig.APIKey)
		if !ok {
			continue
		}
		if configurable, ok := client.(Configurable); ok && providerConfig.Config != nil {
			configurable.Configure(providerConfig.Config)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			states := make(map[string]providerAvailabilityState, len(hashes))
			if bulk, ok := client.(InstantAvailabilityBulkProvider); ok {
				cached, err := bulk.CheckInstantAvailabilityBulk(ctx, hashes)
				if err == nil {
					for _, hash := range hashes {
						states[hash] = providerAvailabilityState{known: true, cached: cached[hash]}
					}
				}
			} else {
				const workers = 8
				jobs := make(chan string)
				var stateMu sync.Mutex
				var workerWG sync.WaitGroup
				for i := 0; i < workers; i++ {
					workerWG.Add(1)
					go func() {
						defer workerWG.Done()
						for hash := range jobs {
							cached, err := client.CheckInstantAvailability(ctx, hash)
							if err == nil {
								stateMu.Lock()
								states[hash] = providerAvailabilityState{known: true, cached: cached}
								stateMu.Unlock()
							}
						}
					}()
				}
				for _, hash := range hashes {
					select {
					case jobs <- hash:
					case <-ctx.Done():
						close(jobs)
						workerWG.Wait()
						return
					}
				}
				close(jobs)
				workerWG.Wait()
			}
			mu.Lock()
			availability[key] = states
			mu.Unlock()
		}()
	}
	wg.Wait()
	return expandResultsByProviderAvailability(results, settings.Streaming.DebridProviders, availability)
}

func providerFilterSettings(base models.FilterSettings, providers []config.DebridProviderSettings, result models.NZBResult) models.FilterSettings {
	providerKey := resultProviderKey(result)
	if providerKey == "" {
		return base
	}
	for _, provider := range providers {
		if configuredProviderKey(provider) != providerKey || provider.Filtering == nil {
			continue
		}
		applyProviderFilterOverrides(&base, *provider.Filtering)
		break
	}
	return base
}

func applyProviderFilterOverrides(dst *models.FilterSettings, src config.ProviderFilterSettings) {
	if src.MaxSizeMovieGB != nil {
		dst.MaxSizeMovieGB = src.MaxSizeMovieGB
	}
	if src.MaxSizeEpisodeGB != nil {
		dst.MaxSizeEpisodeGB = src.MaxSizeEpisodeGB
	}
	if src.MaxResolution != nil {
		dst.MaxResolution = *src.MaxResolution
	}
	if src.HDRDVPolicy != nil {
		dst.HDRDVPolicy = models.HDRDVPolicy(*src.HDRDVPolicy)
	}
	if src.RequiredTerms != nil {
		dst.RequiredTerms = append([]string(nil), src.RequiredTerms...)
	}
	if src.FilterOutTerms != nil {
		dst.FilterOutTerms = append([]string(nil), src.FilterOutTerms...)
	}
}
