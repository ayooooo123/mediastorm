package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlaybackSettingsNormalizeAllowedTrackLanguages(t *testing.T) {
	playback := PlaybackSettings{AllowedTrackLanguages: []string{" ENG ", "'fra'", "eng", ""}}
	playback.NormalizeAllowedTrackLanguages()
	if !reflect.DeepEqual(playback.AllowedTrackLanguages, []string{"eng", "fra"}) {
		t.Fatalf("AllowedTrackLanguages = %#v, want eng/fra", playback.AllowedTrackLanguages)
	}
}

func TestEnsureDefaultHomeShelvesBackfillsSharedShelfLimits(t *testing.T) {
	shelves, changed := EnsureDefaultHomeShelves([]ShelfConfig{
		{ID: "popular-on-server", Name: "Popular"},
		{ID: "recently-watched", Name: "Recent"},
	})
	if !changed {
		t.Fatal("expected shared shelf limits to be backfilled")
	}

	for _, shelf := range shelves {
		if shelf.ID == "popular-on-server" || shelf.ID == "recently-watched" {
			if shelf.Limit != 20 {
				t.Fatalf("%s limit = %d, want 20", shelf.ID, shelf.Limit)
			}
		}
	}
}

func TestEnsureDefaultHomeShelvesAddsDisabledPermanentPrequeue(t *testing.T) {
	shelves, changed := EnsureDefaultHomeShelves([]ShelfConfig{
		{ID: "recently-watched", Name: "Recent", Enabled: true, Order: 4},
	})
	if !changed {
		t.Fatal("expected permanent prequeue shelf to be backfilled")
	}
	for _, shelf := range shelves {
		if shelf.ID == "permanent-prequeue" {
			if shelf.Enabled {
				t.Fatal("permanent prequeue shelf should default disabled")
			}
			if shelf.Name != "Permanent Prequeue" {
				t.Fatalf("unexpected shelf name %q", shelf.Name)
			}
			return
		}
	}
	t.Fatal("permanent prequeue shelf was not added")
}

func TestMigrateLibraryShelfConfigs(t *testing.T) {
	shelves := []ShelfConfig{{ID: "local-library-library-123", Type: "local-library"}}

	if !MigrateLibraryShelfConfigs(shelves) {
		t.Fatal("expected legacy shelf migration")
	}
	if shelves[0].Type != "library" || shelves[0].LibraryID != "library-123" {
		t.Fatalf("unexpected migrated shelf: %+v", shelves[0])
	}
	if MigrateLibraryShelfConfigs(shelves) {
		t.Fatal("expected migration to be idempotent")
	}
}

func TestLoadMigratesCreditsDetectionToCreditsAutoSkip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"playback":{"preferredPlayer":"native","creditsDetection":true}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !settings.Playback.CreditsAutoSkip {
		t.Fatal("expected legacy creditsDetection=true to migrate to creditsAutoSkip=true")
	}
	if settings.Playback.CreditsDetection {
		t.Fatal("expected legacy creditsDetection field to be cleared after migration")
	}
	if settings.Playback.CreditsDetectionEnabled {
		t.Fatal("expected creditsDetectionEnabled to default to false")
	}
}

func TestDefaultSettingsDisablesMatchFrameRate(t *testing.T) {
	settings := DefaultSettings()

	if settings.Playback.MatchFrameRate {
		t.Fatal("expected match frame rate to default to disabled")
	}
}

func TestDefaultSettingsEnablesStreamMigration(t *testing.T) {
	settings := DefaultSettings()

	if !settings.Playback.StreamMigrationEnabled {
		t.Fatal("expected stream migration to default to enabled")
	}
}

func TestDefaultSettingsEnablesLiveClosedCaptionExtraction(t *testing.T) {
	settings := DefaultSettings()

	if !settings.Playback.LiveClosedCaptionExtraction {
		t.Fatal("expected live closed caption extraction to default to enabled")
	}
}

func TestLoadDefaultsMissingLiveClosedCaptionExtractionToEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"playback":{"preferredPlayer":"native"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !settings.Playback.LiveClosedCaptionExtraction {
		t.Fatal("expected missing liveClosedCaptionExtraction to default to enabled")
	}
}

func TestDefaultSettingsDisablesThumbnailGeneration(t *testing.T) {
	settings := DefaultSettings()

	if settings.Playback.Thumbnails.Enabled {
		t.Fatal("expected thumbnail generation to default to disabled")
	}
	if settings.Playback.Thumbnails.Workers != 1 {
		t.Fatalf("thumbnail workers = %d, want 1", settings.Playback.Thumbnails.Workers)
	}
}

func TestLoadBackfillsThumbnailSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"playback":{"preferredPlayer":"native"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Playback.Thumbnails.Enabled {
		t.Fatal("expected thumbnail generation to backfill as disabled")
	}
	if settings.Playback.Thumbnails.Workers != 1 {
		t.Fatalf("thumbnail workers = %d, want 1", settings.Playback.Thumbnails.Workers)
	}
}

func TestDefaultSettingsEnablesCleanPosters(t *testing.T) {
	settings := DefaultSettings()

	if !settings.Display.CleanPosters {
		t.Fatal("expected clean posters to default to enabled")
	}
}

func TestDefaultSettingsEnablesApplicationAnimations(t *testing.T) {
	settings := DefaultSettings()
	if !settings.Display.EnableAnimations {
		t.Fatal("application animations should be enabled by default")
	}
}

func TestSavePreservesDisabledUnreleasedVisibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	manager := NewManager(path)

	settings := DefaultSettings()
	settings.Display.IncludeUnreleasedMoviesInLists = false
	settings.Display.IncludeUnreleasedShowsInLists = false
	settings.Display.IncludeUnreleasedMoviesInSearch = false
	settings.Display.IncludeUnreleasedShowsInSearch = false
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if loaded.Display.IncludeUnreleasedMoviesInLists ||
		loaded.Display.IncludeUnreleasedShowsInLists ||
		loaded.Display.IncludeUnreleasedMoviesInSearch ||
		loaded.Display.IncludeUnreleasedShowsInSearch {
		t.Fatalf("disabled unreleased visibility was not preserved: %+v", loaded.Display)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var persisted map[string]interface{}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode persisted settings: %v", err)
	}
	display, ok := persisted["display"].(map[string]interface{})
	if !ok {
		t.Fatalf("persisted display has type %T, want object", persisted["display"])
	}
	for _, key := range []string{
		"includeUnreleasedMoviesInLists",
		"includeUnreleasedShowsInLists",
		"includeUnreleasedMoviesInSearch",
		"includeUnreleasedShowsInSearch",
	} {
		if value, ok := display[key]; !ok || value != false {
			t.Fatalf("persisted display[%s] = %#v, want false", key, value)
		}
	}
}

func TestDefaultSettingsIncludesDisabledUsenetEnginePresets(t *testing.T) {
	settings := DefaultSettings()

	if len(settings.UsenetEngines) != 4 {
		t.Fatalf("UsenetEngines length = %d, want 4", len(settings.UsenetEngines))
	}
	pathsByType := map[string]string{}
	for _, engine := range settings.UsenetEngines {
		if engine.Enabled {
			t.Fatalf("engine %q should default to disabled", engine.Type)
		}
		pathsByType[engine.Type] = engine.APIPath
	}
	for _, typ := range []string{"nzbdav", "nzbdavex"} {
		if pathsByType[typ] != "/api" {
			t.Fatalf("%s APIPath = %q, want /api", typ, pathsByType[typ])
		}
	}
	if pathsByType["altmount"] != "/sabnzbd/api" {
		t.Fatalf("altmount APIPath = %q, want /sabnzbd/api", pathsByType["altmount"])
	}
	if pathsByType["decypharr"] != "/sabnzbd/api" {
		t.Fatalf("decypharr APIPath = %q, want /sabnzbd/api", pathsByType["decypharr"])
	}
}

func TestLoadBackfillsUsenetEnginePresets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"usenetEngines":[{"name":"My NZBDav","type":"nzbdav","enabled":true,"baseUrl":"http://nzbdav:3000"}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if len(settings.UsenetEngines) != 4 {
		t.Fatalf("UsenetEngines length = %d, want 4: %#v", len(settings.UsenetEngines), settings.UsenetEngines)
	}
	if settings.UsenetEngines[0].Name != "My NZBDav" || settings.UsenetEngines[0].BaseURL != "http://nzbdav:3000" {
		t.Fatalf("existing engine was not preserved: %+v", settings.UsenetEngines[0])
	}
	if settings.UsenetEngines[0].APIPath != "/api" {
		t.Fatalf("existing nzbdav APIPath = %q, want /api", settings.UsenetEngines[0].APIPath)
	}

	seen := map[string]bool{}
	for _, engine := range settings.UsenetEngines {
		seen[engine.Type] = true
	}
	for _, typ := range []string{"altmount", "nzbdav", "nzbdavex", "decypharr"} {
		if !seen[typ] {
			t.Fatalf("missing engine preset %q in %#v", typ, settings.UsenetEngines)
		}
	}
}

func TestManagerAllowsOnlyOneEnabledUsenetEngine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	manager := NewManager(path)
	settings := DefaultSettings()
	settings.UsenetEngines = []UsenetEngineSettings{
		{Name: "AltMount", Type: "altmount", Enabled: true},
		{Name: "NZBDav", Type: "nzbdav", Enabled: true},
		{Name: "Decypharr", Type: "decypharr", Enabled: true},
	}

	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !loaded.UsenetEngines[0].Enabled {
		t.Fatalf("first enabled engine was disabled: %#v", loaded.UsenetEngines)
	}
	for i := 1; i < len(loaded.UsenetEngines); i++ {
		if loaded.UsenetEngines[i].Enabled {
			t.Fatalf("engine %d remains enabled: %#v", i, loaded.UsenetEngines)
		}
	}
}

func TestLoadForcesCleanPostersEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"display":{"cleanPosters":false}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !settings.Display.CleanPosters {
		t.Fatal("expected clean posters to be forced enabled on load")
	}
}

func TestLoadMigratesNavigationTabVisibilitySystemTabs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"ui":{"loadingAnimationEnabled":true},"display":{"navigationTabVisibility":["home","search","lists","live","profiles","downloads"]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !settings.UI.NavigationTabVisibilityIncludesSystemTabs {
		t.Fatal("expected navigation tab visibility migration marker to be set")
	}
	if !containsString(settings.Display.NavigationTabVisibility, "settings") {
		t.Fatalf("navigationTabVisibility = %#v, want settings tab added", settings.Display.NavigationTabVisibility)
	}
	if !containsString(settings.Display.NavigationTabVisibility, "admin") {
		t.Fatalf("navigationTabVisibility = %#v, want admin tab added", settings.Display.NavigationTabVisibility)
	}
}

func TestLoadPreservesHiddenSystemTabsAfterNavigationTabVisibilityMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"ui":{"loadingAnimationEnabled":true,"navigationTabVisibilityIncludesSystemTabs":true},"display":{"navigationTabVisibility":["home","search","lists","live","profiles","downloads"]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if containsString(settings.Display.NavigationTabVisibility, "settings") {
		t.Fatalf("navigationTabVisibility = %#v, settings should remain hidden after migration marker", settings.Display.NavigationTabVisibility)
	}
	if containsString(settings.Display.NavigationTabVisibility, "admin") {
		t.Fatalf("navigationTabVisibility = %#v, admin should remain hidden after migration marker", settings.Display.NavigationTabVisibility)
	}
}

func TestLoadMigratesNavigationTabVisibilityWatchlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"ui":{"loadingAnimationEnabled":true,"navigationTabVisibilityIncludesSystemTabs":true},"display":{"navigationTabVisibility":["home","search","lists","live","profiles","downloads","settings","admin"]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !settings.UI.NavigationTabVisibilityIncludesWatchlist {
		t.Fatal("expected Watchlist navigation visibility migration marker to be set")
	}
	if !containsString(settings.Display.NavigationTabVisibility, "watchlist") {
		t.Fatalf("navigationTabVisibility = %#v, want Watchlist tab added", settings.Display.NavigationTabVisibility)
	}
}

func TestLoadPreservesHiddenWatchlistAfterNavigationTabVisibilityMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"ui":{"loadingAnimationEnabled":true,"navigationTabVisibilityIncludesSystemTabs":true,"navigationTabVisibilityIncludesWatchlist":true},"display":{"navigationTabVisibility":["home","search","lists","live","profiles","downloads","settings","admin"]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if containsString(settings.Display.NavigationTabVisibility, "watchlist") {
		t.Fatalf("navigationTabVisibility = %#v, Watchlist should remain hidden after migration marker", settings.Display.NavigationTabVisibility)
	}
}

func TestLoadDefaultsMissingMatchFrameRateToDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"playback":{"preferredPlayer":"native"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Playback.MatchFrameRate {
		t.Fatal("expected missing matchFrameRate to default to disabled")
	}
}

func TestLoadDefaultsMissingStreamMigrationToEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"playback":{"preferredPlayer":"native"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if !settings.Playback.StreamMigrationEnabled {
		t.Fatal("expected missing streamMigrationEnabled to default to enabled")
	}
}

func TestLoadMigratesYouTubeProxyURLFromMetadataToPlayback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"metadata":{"youtubeProxyUrl":"http://gluetun:8888"},"playback":{"preferredPlayer":"native"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Playback.YouTubeProxyURL != "http://gluetun:8888" {
		t.Fatalf("Playback.YouTubeProxyURL = %q, want migrated proxy", settings.Playback.YouTubeProxyURL)
	}
}

func TestLoadMigratesGeminiAPIKeyToAISettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"metadata":{"geminiApiKey":"legacy-gemini-key","language":"eng"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Metadata.AIProvider != "gemini" {
		t.Fatalf("AIProvider = %q, want gemini", settings.Metadata.AIProvider)
	}
	if settings.Metadata.AIAPIKey != "legacy-gemini-key" {
		t.Fatalf("AIAPIKey = %q, want legacy-gemini-key", settings.Metadata.AIAPIKey)
	}
	if settings.Metadata.GeminiAPIKey != "legacy-gemini-key" {
		t.Fatalf("GeminiAPIKey = %q, want legacy-gemini-key", settings.Metadata.GeminiAPIKey)
	}
}

func TestLoadMigratesMetadataLanguageStringToArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"metadata":{"language":"fra"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if len(settings.Metadata.Language) != 1 || settings.Metadata.Language[0] != "fra" {
		t.Fatalf("Metadata.Language = %#v, want [fra]", settings.Metadata.Language)
	}
	if settings.Metadata.EffectivePrimaryLanguage() != "fra" {
		t.Fatalf("PrimaryLanguage = %q, want fra", settings.Metadata.EffectivePrimaryLanguage())
	}
	if settings.Metadata.PrimaryLanguage != "fra" {
		t.Fatalf("Metadata.PrimaryLanguage = %q, want fra", settings.Metadata.PrimaryLanguage)
	}
}

func TestLoadNormalizesMetadataLanguageArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"metadata":{"language":["fra"," ","FRA","spa"]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	want := []string{"fra", "spa"}
	if len(settings.Metadata.Language) != len(want) {
		t.Fatalf("Metadata.Language = %#v, want %#v", settings.Metadata.Language, want)
	}
	for i := range want {
		if settings.Metadata.Language[i] != want[i] {
			t.Fatalf("Metadata.Language = %#v, want %#v", settings.Metadata.Language, want)
		}
	}
}

func TestSaveNormalizesMetadataLanguageArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings := DefaultSettings()
	settings.Metadata.Language = []string{"", "fra", "FRA", "spa"}
	settings.Metadata.PrimaryLanguage = "fra"

	if err := NewManager(path).Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var saved struct {
		Metadata struct {
			Language        []string `json:"language"`
			PrimaryLanguage string   `json:"primaryLanguage"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("unmarshal saved settings: %v", err)
	}
	want := []string{"fra", "spa"}
	if len(saved.Metadata.Language) != len(want) {
		t.Fatalf("saved metadata.language = %#v, want %#v", saved.Metadata.Language, want)
	}
	for i := range want {
		if saved.Metadata.Language[i] != want[i] {
			t.Fatalf("saved metadata.language = %#v, want %#v", saved.Metadata.Language, want)
		}
	}
	if saved.Metadata.PrimaryLanguage != "fra" {
		t.Fatalf("saved metadata.primaryLanguage = %q, want fra", saved.Metadata.PrimaryLanguage)
	}
}

func TestSaveMirrorsGeminiAIKeyToLegacyField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings := DefaultSettings()
	settings.Metadata.AIProvider = "gemini"
	settings.Metadata.AIAPIKey = "new-gemini-key"

	if err := NewManager(path).Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	reloaded, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if reloaded.Metadata.GeminiAPIKey != "new-gemini-key" {
		t.Fatalf("GeminiAPIKey = %q, want mirrored key", reloaded.Metadata.GeminiAPIKey)
	}
}

func TestLoadDoesNotOverrideExplicitAIProviderWithLegacyGeminiKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"metadata":{"aiProvider":"openai","geminiApiKey":"legacy-gemini-key","language":"eng"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Metadata.AIProvider != "openai" {
		t.Fatalf("AIProvider = %q, want openai", settings.Metadata.AIProvider)
	}
	if settings.Metadata.AIAPIKey != "" {
		t.Fatalf("AIAPIKey = %q, want empty", settings.Metadata.AIAPIKey)
	}
}

func TestLoadBackfillsUnknownTrackPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"filtering":{"unknownTrackPolicy":"invalid"}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.Filtering.UnknownTrackPolicy != UnknownTrackPolicyNone {
		t.Fatalf("UnknownTrackPolicy = %q, want %q", settings.Filtering.UnknownTrackPolicy, UnknownTrackPolicyNone)
	}
}

func TestLoadClampsHomeShelfAndHeroScale(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantShelf float64
		wantHero  float64
	}{
		{name: "missing", raw: `{"homeShelves":{"shelves":[]}}`, wantShelf: 1.0, wantHero: 1.0},
		{name: "too low", raw: `{"homeShelves":{"shelves":[],"homeShelfScale":0.25,"homeHeroScale":0.25}}`, wantShelf: 0.5, wantHero: 0.5},
		{name: "too high", raw: `{"homeShelves":{"shelves":[],"homeShelfScale":1.4,"homeHeroScale":1.4}}`, wantShelf: 1.0, wantHero: 1.0},
		{name: "valid", raw: `{"homeShelves":{"shelves":[],"homeShelfScale":0.75,"homeHeroScale":0.65}}`, wantShelf: 0.75, wantHero: 0.65},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
				t.Fatalf("write settings: %v", err)
			}

			settings, err := NewManager(path).Load()
			if err != nil {
				t.Fatalf("load settings: %v", err)
			}

			if settings.HomeShelves.HomeShelfScale != tt.wantShelf {
				t.Fatalf("HomeShelfScale = %v, want %v", settings.HomeShelves.HomeShelfScale, tt.wantShelf)
			}
			if settings.HomeShelves.HomeHeroScale != tt.wantHero {
				t.Fatalf("HomeHeroScale = %v, want %v", settings.HomeShelves.HomeHeroScale, tt.wantHero)
			}
		})
	}
}

func TestLoadPreservesHomeTopShelfSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"homeShelves":{
		"shelves":[],
		"mobileTopShelfMode":"shelf",
		"mobileTopShelfSourceId":"calendar",
		"tvTopShelfMode":"disabled",
		"tvTopShelfSourceId":"continue-watching"
	}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if settings.HomeShelves.MobileTopShelfMode != "shelf" {
		t.Fatalf("MobileTopShelfMode = %q, want shelf", settings.HomeShelves.MobileTopShelfMode)
	}
	if settings.HomeShelves.MobileTopShelfSourceID != "calendar" {
		t.Fatalf("MobileTopShelfSourceID = %q, want calendar", settings.HomeShelves.MobileTopShelfSourceID)
	}
	if settings.HomeShelves.TVTopShelfMode != "disabled" {
		t.Fatalf("TVTopShelfMode = %q, want disabled", settings.HomeShelves.TVTopShelfMode)
	}
	if settings.HomeShelves.TVTopShelfSourceID != "continue-watching" {
		t.Fatalf("TVTopShelfSourceID = %q, want continue-watching", settings.HomeShelves.TVTopShelfSourceID)
	}
}

func TestLoadDisablesExperimentalTonightShelf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"homeShelves":{"shelves":[
		{"id":"continue-watching","name":"Continue Watching","enabled":true,"order":1},
		{"id":"tonight","name":"Tonight","enabled":true,"order":2}
	]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	for _, shelf := range settings.HomeShelves.Shelves {
		if shelf.ID == "tonight" {
			if shelf.Enabled {
				t.Fatal("expected startup migration to disable tonight shelf")
			}
			return
		}
	}
	t.Fatal("expected tonight shelf to remain present after migration")
}

func TestLoadBackfillsStreamingServicesAndLiveFavoritesHomeShelves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"homeShelves":{"shelves":[
		{"id":"continue-watching","name":"Continue Watching","enabled":true,"order":1},
		{"id":"trending-tv","name":"Trending TV Shows","enabled":true,"order":6}
	]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	var streamingShelf *ShelfConfig
	var liveFavoritesShelf *ShelfConfig
	trendingTVOrder := -1
	for i := range settings.HomeShelves.Shelves {
		if settings.HomeShelves.Shelves[i].ID == "streaming-services" {
			streamingShelf = &settings.HomeShelves.Shelves[i]
		}
		if settings.HomeShelves.Shelves[i].ID == "live-favorites" {
			liveFavoritesShelf = &settings.HomeShelves.Shelves[i]
		}
		if settings.HomeShelves.Shelves[i].ID == "trending-tv" {
			trendingTVOrder = settings.HomeShelves.Shelves[i].Order
		}
	}
	if streamingShelf == nil {
		t.Fatal("expected streaming-services shelf to be backfilled")
	}
	if !streamingShelf.Enabled {
		t.Fatal("expected streaming-services shelf to default enabled")
	}
	if streamingShelf.Order != trendingTVOrder+1 {
		t.Fatalf("streaming-services order = %d, want after trending-tv order %d", streamingShelf.Order, trendingTVOrder)
	}
	if liveFavoritesShelf == nil {
		t.Fatal("expected live-favorites shelf to be backfilled")
	}
	if liveFavoritesShelf.Enabled {
		t.Fatal("expected live-favorites shelf to default disabled")
	}
	if liveFavoritesShelf.Order != streamingShelf.Order+1 {
		t.Fatalf("live-favorites order = %d, want after streaming-services order %d", liveFavoritesShelf.Order, streamingShelf.Order)
	}
}

func TestLoadMigratesLegacyGeneratedTMDBShelfNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"homeShelves":{"shelves":[
		{"id":"list","name":"TMDB List","type":"tmdb","tmdbSourceType":"public-list"},
		{"id":"collection","name":"TMDB Movie Collection","type":"tmdb","tmdbSourceType":"movie-collection"},
		{"id":"discover","name":"TMDB Discover","type":"tmdb","tmdbSourceType":"custom-discover"},
		{"id":"custom","name":"My TMDB Discover","type":"tmdb","tmdbSourceType":"custom-discover"}
	]}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	got := make(map[string]string)
	for _, shelf := range settings.HomeShelves.Shelves {
		got[shelf.ID] = shelf.Name
	}
	want := map[string]string{
		"list":       "List",
		"collection": "Movie Collection",
		"discover":   "Discover",
		"custom":     "My TMDB Discover",
	}
	for id, name := range want {
		if got[id] != name {
			t.Fatalf("shelf %q name = %q, want %q", id, got[id], name)
		}
	}
}

func TestLoadMigratesLegacyLiveSettingsToFirstSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{
		"live": {
			"mode": "m3u",
			"playlistUrl": "http://example.com/live.m3u",
			"maxStreams": 3,
			"playlistCacheTtlHours": 8,
			"probeSizeMb": 12,
			"analyzeDurationSec": 6,
			"lowLatency": true,
			"streamFormat": "direct",
			"filtering": {
				"enabledCategories": ["News"],
				"maxChannels": 50
			},
			"epg": {
				"enabled": true,
				"xmltvUrl": "http://example.com/epg.xml",
				"refreshIntervalHours": 4,
				"retentionDays": 2,
				"timeOffsetMinutes": 30
			}
		}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	settings, err := NewManager(path).Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	if len(settings.Live.Sources) != 1 {
		t.Fatalf("Live.Sources length = %d, want 1", len(settings.Live.Sources))
	}
	src := settings.Live.Sources[0]
	if src.Name != "Default" || src.PlaylistURL != "http://example.com/live.m3u" {
		t.Fatalf("migrated source = %+v, want default source with legacy playlist URL", src)
	}
	if src.Enabled == nil || !*src.Enabled {
		t.Fatalf("source enabled = %v, want true", src.Enabled)
	}
	if src.MaxStreams != 3 || src.StreamFormat != "direct" || !src.LowLatency {
		t.Fatalf("source tuning not migrated: %+v", src)
	}
	if src.Filtering.MaxChannels != 50 || len(src.Filtering.EnabledCategories) != 1 || src.Filtering.EnabledCategories[0] != "News" {
		t.Fatalf("source filtering not migrated: %+v", src.Filtering)
	}
	if !src.EPG.Enabled || src.EPG.XmltvUrl != "http://example.com/epg.xml" || src.EPG.TimeOffsetMinutes != 30 {
		t.Fatalf("source EPG not migrated: %+v", src.EPG)
	}
}

func TestSavePreservesClearedLiveSourceProxyURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	manager := NewManager(path)

	enabled := true
	settings := DefaultSettings()
	settings.Live.ProxyURL = "socks5://127.0.0.1:18080"
	settings.Live.Sources = []LivePlaylistSource{
		{
			ID:          "default",
			Name:        "Default",
			Mode:        "m3u",
			PlaylistURL: "http://example.com/live.m3u",
			ProxyURL:    "",
			Enabled:     &enabled,
		},
	}

	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(loaded.Live.Sources) != 1 {
		t.Fatalf("Live.Sources length = %d, want 1", len(loaded.Live.Sources))
	}
	if loaded.Live.Sources[0].ProxyURL != "" {
		t.Fatalf("Live.Sources[0].ProxyURL = %q, want cleared value", loaded.Live.Sources[0].ProxyURL)
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func TestNormalizeHardwareAcceleration(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "", want: "auto"},
		{input: " AUTO ", want: "auto"},
		{input: "NVENC", want: "nvenc"},
		{input: "qsv", want: "qsv"},
		{input: "vaapi", want: "vaapi"},
		{input: "VideoToolbox", want: "videotoolbox"},
		{input: "none", want: "none"},
	} {
		settings := TransmuxSettings{HardwareAcceleration: test.input}
		if err := settings.NormalizeHardwareAcceleration(); err != nil {
			t.Fatalf("NormalizeHardwareAcceleration(%q): %v", test.input, err)
		}
		if settings.HardwareAcceleration != test.want {
			t.Fatalf("NormalizeHardwareAcceleration(%q) = %q, want %q", test.input, settings.HardwareAcceleration, test.want)
		}
	}

	settings := TransmuxSettings{HardwareAcceleration: "cuda"}
	if err := settings.NormalizeHardwareAcceleration(); err == nil {
		t.Fatal("expected unsupported hardware acceleration value to fail")
	}
}
