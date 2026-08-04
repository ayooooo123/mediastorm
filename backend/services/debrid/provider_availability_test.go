package debrid

import (
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestExpandResultsByProviderAvailabilityCreatesCachedProviderVariants(t *testing.T) {
	providers := []config.DebridProviderSettings{
		{Provider: "torbox", APIKey: "key", Enabled: true},
		{Provider: "realdebrid", APIKey: "key", Enabled: true},
	}
	result := models.NZBResult{
		Title: "Movie.2026.1080p",
		GUID:  "magnet:abc",
		Attributes: map[string]string{
			"infoHash": "abc",
		},
		ServiceType: models.ServiceTypeDebrid,
	}
	availability := map[string]map[string]providerAvailabilityState{
		"torbox":     {"abc": {known: true, cached: true}},
		"realdebrid": {"abc": {known: true, cached: true}},
	}

	got := expandResultsByProviderAvailability([]models.NZBResult{result}, providers, availability)
	if len(got) != 2 {
		t.Fatalf("expanded result count = %d, want 2", len(got))
	}
	if got[0].Attributes["provider"] != "torbox" || got[1].Attributes["provider"] != "realdebrid" {
		t.Fatalf("expanded providers = %q, %q", got[0].Attributes["provider"], got[1].Attributes["provider"])
	}
	if got[0].GUID == got[1].GUID {
		t.Fatalf("provider variants must have distinct GUIDs: %q", got[0].GUID)
	}
}

func TestExpandResultsByProviderAvailabilityDropsKnownUncachedResult(t *testing.T) {
	providers := []config.DebridProviderSettings{{Provider: "realdebrid", APIKey: "key", Enabled: true}}
	result := models.NZBResult{Attributes: map[string]string{"infoHash": "abc"}, ServiceType: models.ServiceTypeDebrid}
	availability := map[string]map[string]providerAvailabilityState{
		"realdebrid": {"abc": {known: true, cached: false}},
	}
	if got := expandResultsByProviderAvailability([]models.NZBResult{result}, providers, availability); len(got) != 0 {
		t.Fatalf("uncached result count = %d, want 0", len(got))
	}
}

func TestExpandResultsByProviderAvailabilityRetainsUnknownResult(t *testing.T) {
	providers := []config.DebridProviderSettings{{Provider: "realdebrid", APIKey: "key", Enabled: true}}
	result := models.NZBResult{Attributes: map[string]string{"infoHash": "abc"}, ServiceType: models.ServiceTypeDebrid}
	availability := map[string]map[string]providerAvailabilityState{"realdebrid": {}}
	if got := expandResultsByProviderAvailability([]models.NZBResult{result}, providers, availability); len(got) != 1 {
		t.Fatalf("unknown availability result count = %d, want fallback result", len(got))
	}
}

func TestExpandResultsByProviderAvailabilityRecognizesPreResolvedProviderAlias(t *testing.T) {
	providers := []config.DebridProviderSettings{{Name: "Real-Debrid", Provider: "realdebrid", APIKey: "key", Enabled: true}}
	result := models.NZBResult{
		GUID:        "direct:1",
		Attributes:  map[string]string{"preresolved": "true", "tracker": "RD+"},
		ServiceType: models.ServiceTypeDebrid,
	}
	got := expandResultsByProviderAvailability([]models.NZBResult{result}, providers, nil)
	if len(got) != 1 || got[0].Attributes["provider"] != "realdebrid" {
		t.Fatalf("pre-resolved provider = %#v, want realdebrid", got)
	}
}

func TestProviderFilterSettingsOnlyOverridesMatchingProvider(t *testing.T) {
	base := models.FilterSettings{MaxSizeMovieGB: models.FloatPtr(25), FilterOutTerms: []string{"shared"}}
	providers := []config.DebridProviderSettings{
		{Provider: "realdebrid", Filtering: &config.ProviderFilterSettings{FilterOutTerms: []string{"restricted"}}},
	}
	rd := models.NZBResult{Attributes: map[string]string{"provider": "realdebrid"}}
	torbox := models.NZBResult{Attributes: map[string]string{"provider": "torbox"}}

	if got := providerFilterSettings(base, providers, rd).FilterOutTerms; len(got) != 1 || got[0] != "restricted" {
		t.Fatalf("Real-Debrid filter terms = %#v, want provider override", got)
	}
	if got := models.FloatVal(providerFilterSettings(base, providers, rd).MaxSizeMovieGB, 0); got != 25 {
		t.Fatalf("unrelated provider max movie size = %v, want inherited 25", got)
	}
	if got := providerFilterSettings(base, providers, torbox).FilterOutTerms; len(got) != 1 || got[0] != "shared" {
		t.Fatalf("Torbox filter terms = %#v, want shared filters", got)
	}
}
