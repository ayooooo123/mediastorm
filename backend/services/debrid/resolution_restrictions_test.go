package debrid

import (
	"context"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestRealDebridRestrictionForCandidate(t *testing.T) {
	settings := config.Settings{Filtering: config.FilterSettings{RealDebridRestrictedTermsFilterEnabled: true}}
	realDebrid := config.DebridProviderSettings{Name: "Real Debrid", Provider: "realdebrid"}
	torbox := config.DebridProviderSettings{Name: "TorBox", Provider: "torbox"}

	tests := []struct {
		name       string
		provider   config.DebridProviderSettings
		title      string
		rawTitle   string
		restricted bool
	}{
		{name: "web dl", provider: realDebrid, title: "Movie.2026.1080p.WEB-DL.H264-GROUP", restricted: true},
		{name: "web h264 raw title", provider: realDebrid, title: "Movie 2026", rawTitle: "Movie.2026.WEB.H264-GROUP", restricted: true},
		{name: "web h264 spaced", provider: realDebrid, title: "Movie 2026 1080p WEB H264-GROUP", restricted: false},
		{name: "web x264 spaced", provider: realDebrid, title: "Movie 2026 1080p WEB x264-GROUP", restricted: false},
		{name: "case-only raw title difference blocks", provider: realDebrid, title: "Movie.2026.WEB.h264-GROUP", rawTitle: "Movie.2026.WEB.H264-GROUP", restricted: true},
		{name: "case-only raw title difference passes", provider: realDebrid, title: "Movie.2026.WEB.H264-GROUP", rawTitle: "Movie.2026.WEB.h264-GROUP", restricted: false},
		{name: "raw separators are authoritative", provider: realDebrid, title: "Movie.2026.WEB.H264-GROUP", rawTitle: "Movie 2026 WEB H264-GROUP", restricted: false},
		{name: "blank raw title falls back", provider: realDebrid, title: "Movie.2026.WEB.H264-GROUP", rawTitle: "  ", restricted: true},
		{name: "web rip now eligible", provider: realDebrid, title: "Movie.2026.1080p.WEBRip.x264-GROUP", restricted: false},
		{name: "bluray x264 now eligible", provider: realDebrid, title: "Movie.2026.1080p.BluRay.x264-GROUP", restricted: false},
		{name: "remux remains eligible", provider: realDebrid, title: "Movie.2026.2160p.UHD.BluRay.REMUX", restricted: false},
		{name: "other provider remains eligible", provider: torbox, title: "Movie.2026.1080p.WEB-DL.H264-GROUP", restricted: false},
		{name: "other provider ignores restricted raw title", provider: torbox, title: "Movie 2026", rawTitle: "Movie.2026.WEB.H264-GROUP", restricted: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := models.NZBResult{Title: tt.title, Attributes: map[string]string{"raw_title": tt.rawTitle}}
			err := realDebridRestrictionForCandidate(settings, tt.provider, candidate)
			if (err != nil) != tt.restricted {
				t.Fatalf("restriction error = %v, want restricted=%t", err, tt.restricted)
			}
			if err != nil && !IsBlockedContentError(err) {
				t.Fatalf("restriction error should be classified as blocked content: %v", err)
			}
		})
	}

	settings.Filtering.RealDebridRestrictedTermsFilterEnabled = false
	if err := realDebridRestrictionForCandidate(settings, realDebrid, models.NZBResult{Title: "Movie.2026.WEB-DL.H264"}); err != nil {
		t.Fatalf("disabled filter returned error: %v", err)
	}

	settings.Filtering.RealDebridRestrictedTermsFilterEnabled = true
	if err := realDebridRestrictionForCandidate(settings, realDebrid, models.NZBResult{
		Title:      "Movie.2026.WEB-DL.H264",
		Attributes: map[string]string{realDebridRestrictedTermsFilterAttribute: "false"},
	}); err != nil {
		t.Fatalf("scoped false override returned error: %v", err)
	}
	settings.Filtering.RealDebridRestrictedTermsFilterEnabled = false
	if err := realDebridRestrictionForCandidate(settings, realDebrid, models.NZBResult{
		Title:      "Movie.2026.WEB-DL.H264",
		Attributes: map[string]string{realDebridRestrictedTermsFilterAttribute: "true"},
	}); err == nil {
		t.Fatal("expected scoped true override to restrict the result")
	}
}

func TestEffectiveRestrictionSettingCascadesGlobalProfileClient(t *testing.T) {
	global := config.DefaultSettings()
	global.Filtering.RealDebridRestrictedTermsFilterEnabled = true
	profileValue := false
	clientValue := true

	svc := &SearchService{
		userSettings: stubUserSettings{settings: &models.UserSettings{Filtering: models.FilterSettings{
			RealDebridRestrictedTermsFilterEnabled: &profileValue,
		}}},
		clientSettings: stubClientSettings{settings: &models.ClientFilterSettings{
			RealDebridRestrictedTermsFilterEnabled: &clientValue,
		}},
	}

	effective, _ := svc.getEffectiveFilterSettings("profile-1", "client-1", global)
	if !models.BoolVal(effective.RealDebridRestrictedTermsFilterEnabled, false) {
		t.Fatal("expected client true to override profile false and global true")
	}

	svc.clientSettings = nil
	effective, _ = svc.getEffectiveFilterSettings("profile-1", "", global)
	if models.BoolVal(effective.RealDebridRestrictedTermsFilterEnabled, true) {
		t.Fatal("expected profile false to override global true")
	}
}

func TestResolveSkipsRestrictedRealDebridButUsesTorbox(t *testing.T) {
	realDebridMock := &mockProvider{name: "realdebrid"}
	fallbackProviderType := "testprovider_restriction_fallback"
	torboxMock := &mockProvider{
		name:            "torbox",
		status:          "downloaded",
		torrentFilename: "Movie.2026.1080p.WEB-DL.H264-GROUP",
		files: []File{
			{ID: 1, Path: "Movie.2026.1080p.WEB-DL.H264-GROUP.mkv", Bytes: 1_000_000, Selected: 1},
		},
		links: []string{"1:1"},
	}
	RegisterProvider(fallbackProviderType, func(string) Provider { return torboxMock })

	tmpDir := t.TempDir()
	mgr := config.NewManager(tmpDir + "/settings.json")
	if err := mgr.Save(config.Settings{
		Filtering: config.FilterSettings{RealDebridRestrictedTermsFilterEnabled: true},
		Streaming: config.StreamingSettings{DebridProviders: []config.DebridProviderSettings{
			{Name: "Real Debrid", Provider: "realdebrid", APIKey: "rd-key", Enabled: true},
			{Name: "TorBox", Provider: fallbackProviderType, APIKey: "tb-key", Enabled: true},
		}},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	svc := NewPlaybackService(mgr, nil)

	resolution, err := svc.Resolve(context.Background(), models.NZBResult{
		Title:       "Movie.2026.1080p.WEB-DL.H264-GROUP",
		Link:        "magnet:?xt=urn:btih:abcdef1234567890",
		ServiceType: models.ServiceTypeDebrid,
	})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if resolution.DebridProvider != "torbox" {
		t.Fatalf("DebridProvider = %q, want torbox", resolution.DebridProvider)
	}
	if got := atomic.LoadInt64(&realDebridMock.addMagnetCalls); got != 0 {
		t.Fatalf("Real-Debrid AddMagnet calls = %d, want 0", got)
	}
	if got := atomic.LoadInt64(&torboxMock.addMagnetCalls); got != 1 {
		t.Fatalf("TorBox AddMagnet calls = %d, want 1", got)
	}
}
