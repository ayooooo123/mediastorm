package handlers

import (
	"encoding/json"
	"testing"

	"novastream/config"
)

func TestRedactSettings(t *testing.T) {
	s := config.Settings{
		Server: config.ServerSettings{
			Host:           "0.0.0.0",
			Port:           7777,
			HomepageAPIKey: "secret-homepage-key",
		},
		Usenet: []config.UsenetSettings{
			{Name: "provider1", Host: "news.example.com", Password: "usenet-pass"},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzb", APIKey: "indexer-key"},
		},
		TorrentScrapers: []config.TorrentScraperConfig{
			{Name: "jackett", APIKey: "scraper-key"},
		},
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   "tvdb-key",
			TMDBAPIKey:   "tmdb-key",
			AIAPIKey:     "ai-key",
			GeminiAPIKey: "gemini-key",
		},
		Playback: config.PlaybackSettings{
			YouTubeProxyURL: "http://user:pass@gluetun:8888",
		},
		MDBList: config.MDBListSettings{
			APIKey: "mdblist-key",
		},
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{ID: "t1", ClientSecret: "trakt-secret", AccessToken: "trakt-access", RefreshToken: "trakt-refresh"},
			},
		},
		Scrob: config.ScrobSettings{Accounts: []config.ScrobAccount{{ID: "s1", APIKey: "scrob-key", Password: "scrob-password", TOTPSecret: "scrob-totp"}}},
		Plex: config.PlexSettings{
			Accounts: []config.PlexAccount{
				{ID: "p1", AuthToken: "plex-token"},
			},
		},
		Jellyfin: config.JellyfinSettings{
			Accounts: []config.JellyfinAccount{
				{ID: "j1", Token: "jellyfin-token"},
			},
		},
		Database: config.DatabaseSettings{
			URL: "postgres://user:secret@localhost:5432/db",
		},
	}

	redactSettings(&s)

	const redacted = "••••••••"

	// Verify sensitive fields are redacted
	if s.Server.HomepageAPIKey != redacted {
		t.Errorf("HomepageAPIKey not redacted: %q", s.Server.HomepageAPIKey)
	}
	if s.Usenet[0].Password != redacted {
		t.Errorf("Usenet password not redacted: %q", s.Usenet[0].Password)
	}
	if s.Indexers[0].APIKey != redacted {
		t.Errorf("Indexer APIKey not redacted: %q", s.Indexers[0].APIKey)
	}
	if s.TorrentScrapers[0].APIKey != redacted {
		t.Errorf("TorrentScraper APIKey not redacted: %q", s.TorrentScrapers[0].APIKey)
	}
	if s.Metadata.TVDBAPIKey != redacted {
		t.Errorf("TVDBAPIKey not redacted: %q", s.Metadata.TVDBAPIKey)
	}
	if s.Metadata.TMDBAPIKey != redacted {
		t.Errorf("TMDBAPIKey not redacted: %q", s.Metadata.TMDBAPIKey)
	}
	if s.Metadata.AIAPIKey != redacted {
		t.Errorf("AIAPIKey not redacted: %q", s.Metadata.AIAPIKey)
	}
	if s.Metadata.GeminiAPIKey != redacted {
		t.Errorf("GeminiAPIKey not redacted: %q", s.Metadata.GeminiAPIKey)
	}
	if s.Playback.YouTubeProxyURL != redacted {
		t.Errorf("YouTubeProxyURL not redacted: %q", s.Playback.YouTubeProxyURL)
	}
	if s.MDBList.APIKey != redacted {
		t.Errorf("MDBList APIKey not redacted: %q", s.MDBList.APIKey)
	}
	if s.Trakt.Accounts[0].ClientSecret != redacted {
		t.Errorf("Trakt account ClientSecret not redacted: %q", s.Trakt.Accounts[0].ClientSecret)
	}
	if s.Trakt.Accounts[0].AccessToken != redacted {
		t.Errorf("Trakt account AccessToken not redacted: %q", s.Trakt.Accounts[0].AccessToken)
	}
	if s.Trakt.Accounts[0].RefreshToken != redacted {
		t.Errorf("Trakt account RefreshToken not redacted: %q", s.Trakt.Accounts[0].RefreshToken)
	}
	if s.Scrob.Accounts[0].APIKey != redacted || s.Scrob.Accounts[0].Password != redacted || s.Scrob.Accounts[0].TOTPSecret != redacted {
		t.Errorf("Scrob credentials not redacted: key=%q password=%q totp=%q", s.Scrob.Accounts[0].APIKey, s.Scrob.Accounts[0].Password, s.Scrob.Accounts[0].TOTPSecret)
	}
	if s.Plex.Accounts[0].AuthToken != redacted {
		t.Errorf("Plex account AuthToken not redacted: %q", s.Plex.Accounts[0].AuthToken)
	}
	if s.Jellyfin.Accounts[0].Token != redacted {
		t.Errorf("Jellyfin account Token not redacted: %q", s.Jellyfin.Accounts[0].Token)
	}
	if s.Database.URL != redacted {
		t.Errorf("Database URL not redacted: %q", s.Database.URL)
	}

	// Verify non-sensitive fields are untouched
	if s.Server.Host != "0.0.0.0" {
		t.Errorf("Host was modified: %q", s.Server.Host)
	}
	if s.Server.Port != 7777 {
		t.Errorf("Port was modified: %d", s.Server.Port)
	}
	if s.Usenet[0].Name != "provider1" {
		t.Errorf("Usenet name was modified: %q", s.Usenet[0].Name)
	}
	if s.Trakt.Accounts[0].ID != "t1" {
		t.Errorf("Trakt account ID was modified: %q", s.Trakt.Accounts[0].ID)
	}
	if s.Plex.Accounts[0].ID != "p1" {
		t.Errorf("Plex account ID was modified: %q", s.Plex.Accounts[0].ID)
	}
	if s.Jellyfin.Accounts[0].ID != "j1" {
		t.Errorf("Jellyfin account ID was modified: %q", s.Jellyfin.Accounts[0].ID)
	}
}

func TestPreserveRedactedFields_RestoresRealCredentials(t *testing.T) {
	existing := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   "real-tvdb-key",
			TMDBAPIKey:   "real-tmdb-key",
			AIAPIKey:     "real-ai-key",
			GeminiAPIKey: "real-gemini-key",
		},
		Playback: config.PlaybackSettings{
			YouTubeProxyURL: "http://real-proxy:8888",
		},
		Usenet: []config.UsenetSettings{
			{Name: "provider1", Password: "real-password"},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzb", APIKey: "real-indexer-key"},
		},
		MDBList: config.MDBListSettings{
			APIKey: "real-mdblist-key",
		},
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{ID: "t1", ClientSecret: "real-trakt-secret", AccessToken: "real-trakt-access", RefreshToken: "real-trakt-refresh"},
			},
		},
		Plex: config.PlexSettings{
			Accounts: []config.PlexAccount{
				{ID: "p1", AuthToken: "real-plex-token"},
			},
		},
		Jellyfin: config.JellyfinSettings{
			Accounts: []config.JellyfinAccount{
				{ID: "j1", Token: "real-jellyfin-token"},
			},
		},
		Database: config.DatabaseSettings{
			URL: "postgres://user:secret@localhost:5432/db",
		},
	}

	// Simulate a non-master user saving back redacted settings
	incoming := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey:   redactedPlaceholder,
			TMDBAPIKey:   redactedPlaceholder,
			AIAPIKey:     redactedPlaceholder,
			GeminiAPIKey: redactedPlaceholder,
		},
		Playback: config.PlaybackSettings{
			YouTubeProxyURL: redactedPlaceholder,
		},
		Usenet: []config.UsenetSettings{
			{Name: "provider1", Password: redactedPlaceholder},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzb", APIKey: redactedPlaceholder},
		},
		MDBList: config.MDBListSettings{
			APIKey: redactedPlaceholder,
		},
		Trakt: config.TraktSettings{
			Accounts: []config.TraktAccount{
				{ID: "t1", ClientSecret: redactedPlaceholder, AccessToken: redactedPlaceholder, RefreshToken: redactedPlaceholder},
			},
		},
		Plex: config.PlexSettings{
			Accounts: []config.PlexAccount{
				{ID: "p1", AuthToken: redactedPlaceholder},
			},
		},
		Jellyfin: config.JellyfinSettings{
			Accounts: []config.JellyfinAccount{
				{ID: "j1", Token: redactedPlaceholder},
			},
		},
		Database: config.DatabaseSettings{
			URL: redactedPlaceholder,
		},
	}

	preserveRedactedFields(&incoming, &existing)

	// Verify real values are restored
	if incoming.Metadata.TVDBAPIKey != "real-tvdb-key" {
		t.Errorf("TVDBAPIKey not restored: got %q", incoming.Metadata.TVDBAPIKey)
	}
	if incoming.Metadata.TMDBAPIKey != "real-tmdb-key" {
		t.Errorf("TMDBAPIKey not restored: got %q", incoming.Metadata.TMDBAPIKey)
	}
	if incoming.Metadata.AIAPIKey != "real-ai-key" {
		t.Errorf("AIAPIKey not restored: got %q", incoming.Metadata.AIAPIKey)
	}
	if incoming.Playback.YouTubeProxyURL != "http://real-proxy:8888" {
		t.Errorf("YouTubeProxyURL not restored: got %q", incoming.Playback.YouTubeProxyURL)
	}
	if incoming.Usenet[0].Password != "real-password" {
		t.Errorf("Usenet password not restored: got %q", incoming.Usenet[0].Password)
	}
	if incoming.Indexers[0].APIKey != "real-indexer-key" {
		t.Errorf("Indexer APIKey not restored: got %q", incoming.Indexers[0].APIKey)
	}
	if incoming.MDBList.APIKey != "real-mdblist-key" {
		t.Errorf("MDBList APIKey not restored: got %q", incoming.MDBList.APIKey)
	}
	if incoming.Trakt.Accounts[0].ClientSecret != "real-trakt-secret" {
		t.Errorf("Trakt account ClientSecret not restored: got %q", incoming.Trakt.Accounts[0].ClientSecret)
	}
	if incoming.Trakt.Accounts[0].AccessToken != "real-trakt-access" {
		t.Errorf("Trakt account AccessToken not restored: got %q", incoming.Trakt.Accounts[0].AccessToken)
	}
	if incoming.Trakt.Accounts[0].RefreshToken != "real-trakt-refresh" {
		t.Errorf("Trakt account RefreshToken not restored: got %q", incoming.Trakt.Accounts[0].RefreshToken)
	}
	if incoming.Plex.Accounts[0].AuthToken != "real-plex-token" {
		t.Errorf("Plex account AuthToken not restored: got %q", incoming.Plex.Accounts[0].AuthToken)
	}
	if incoming.Jellyfin.Accounts[0].Token != "real-jellyfin-token" {
		t.Errorf("Jellyfin account Token not restored: got %q", incoming.Jellyfin.Accounts[0].Token)
	}
	if incoming.Database.URL != "postgres://user:secret@localhost:5432/db" {
		t.Errorf("Database URL not restored: got %q", incoming.Database.URL)
	}
}

func TestPreserveRedactedFields_AllowsRealUpdates(t *testing.T) {
	existing := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey: "old-key",
			TMDBAPIKey: "old-tmdb",
		},
	}

	// Master user provides a new real key (not the placeholder)
	incoming := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey: "brand-new-key",
			TMDBAPIKey: redactedPlaceholder, // unchanged
		},
	}

	preserveRedactedFields(&incoming, &existing)

	if incoming.Metadata.TVDBAPIKey != "brand-new-key" {
		t.Errorf("should accept new key, got %q", incoming.Metadata.TVDBAPIKey)
	}
	if incoming.Metadata.TMDBAPIKey != "old-tmdb" {
		t.Errorf("redacted field should be restored, got %q", incoming.Metadata.TMDBAPIKey)
	}
}

func TestRedactSettings_EmptyFieldsNotRedacted(t *testing.T) {
	s := config.Settings{
		Metadata: config.MetadataSettings{
			TVDBAPIKey: "",
			TMDBAPIKey: "has-a-key",
		},
	}

	redactSettings(&s)

	if s.Metadata.TVDBAPIKey != "" {
		t.Errorf("empty TVDBAPIKey should stay empty, got %q", s.Metadata.TVDBAPIKey)
	}
	if s.Metadata.TMDBAPIKey != "••••••••" {
		t.Errorf("non-empty TMDBAPIKey should be redacted, got %q", s.Metadata.TMDBAPIKey)
	}
}

func TestRedactSettings_LiveURLsAndAccountKeys(t *testing.T) {
	s := config.Settings{
		MDBList: config.MDBListSettings{
			Accounts: []config.MDBListAccount{{ID: "mdb-1", APIKey: "account-key"}},
		},
		Live: config.LiveSettings{
			PlaylistURL:    "https://live.example/playlist.m3u?token=secret",
			ManifestURL:    "https://addon.example/manifest.json?key=secret",
			ProxyURL:       "http://proxy-user:proxy-pass@proxy.example:8080",
			XtreamHost:     "https://xtream.example",
			XtreamUsername: "xtream-user",
			XtreamPassword: "xtream-pass",
			EPG: config.EPGSettings{
				XmltvUrl: "https://epg.example/guide.xml?token=secret",
				Sources:  []config.EPGSource{{ID: "epg-1", URL: "https://epg.example/second.xml?key=secret"}},
			},
			Sources: []config.LivePlaylistSource{{
				ID: "source-1", PlaylistURL: "https://source.example/list?token=secret", ManifestURL: "https://source.example/manifest?key=secret",
				ProxyURL: "http://user:pass@source-proxy.example", XtreamHost: "https://source-xtream.example", XtreamUsername: "user", XtreamPassword: "pass",
				EPG: config.EPGSettings{XmltvUrl: "https://source-epg.example/guide?token=secret", Sources: []config.EPGSource{{URL: "https://source-epg.example/second?key=secret"}}},
			}},
			PlaylistSources: []config.LivePlaylistSource{{
				ID: "playlist-source-1", PlaylistURL: "https://legacy.example/list?token=secret", ManifestURL: "https://legacy.example/manifest?key=secret",
				ProxyURL: "http://user:pass@legacy-proxy.example", XtreamHost: "https://legacy-xtream.example", XtreamUsername: "user", XtreamPassword: "pass",
				EPG: config.EPGSettings{XmltvUrl: "https://legacy-epg.example/guide?token=secret", Sources: []config.EPGSource{{URL: "https://legacy-epg.example/second?key=secret"}}},
			}},
		},
	}

	redactSettings(&s)

	values := []string{
		s.MDBList.Accounts[0].APIKey,
		s.Live.PlaylistURL, s.Live.ManifestURL, s.Live.ProxyURL, s.Live.XtreamHost, s.Live.XtreamUsername, s.Live.XtreamPassword,
		s.Live.EPG.XmltvUrl, s.Live.EPG.Sources[0].URL,
		s.Live.Sources[0].PlaylistURL, s.Live.Sources[0].ManifestURL, s.Live.Sources[0].ProxyURL,
		s.Live.Sources[0].XtreamHost, s.Live.Sources[0].XtreamUsername, s.Live.Sources[0].XtreamPassword,
		s.Live.Sources[0].EPG.XmltvUrl, s.Live.Sources[0].EPG.Sources[0].URL,
		s.Live.PlaylistSources[0].PlaylistURL, s.Live.PlaylistSources[0].ManifestURL, s.Live.PlaylistSources[0].ProxyURL,
		s.Live.PlaylistSources[0].XtreamHost, s.Live.PlaylistSources[0].XtreamUsername, s.Live.PlaylistSources[0].XtreamPassword,
		s.Live.PlaylistSources[0].EPG.XmltvUrl, s.Live.PlaylistSources[0].EPG.Sources[0].URL,
	}
	for i, got := range values {
		if got != redactedPlaceholder {
			t.Errorf("sensitive value %d not redacted: %q", i, got)
		}
	}
}

func TestPreserveRedactedFields_LiveURLsAndAccountKeys(t *testing.T) {
	existing := config.Settings{
		MDBList: config.MDBListSettings{Accounts: []config.MDBListAccount{{ID: "mdb-1", APIKey: "real-account-key"}}},
		Scrob:   config.ScrobSettings{Accounts: []config.ScrobAccount{{ID: "scrob-1", APIKey: "real-scrob-key", Password: "real-scrob-password", TOTPSecret: "real-scrob-totp"}}},
		Live: config.LiveSettings{
			PlaylistURL: "real-playlist", ManifestURL: "real-manifest", ProxyURL: "real-proxy", XtreamHost: "real-host", XtreamUsername: "real-user", XtreamPassword: "real-pass",
			EPG: config.EPGSettings{XmltvUrl: "real-xmltv", Sources: []config.EPGSource{{URL: "real-epg-source"}}},
			Sources: []config.LivePlaylistSource{{
				PlaylistURL: "real-source-playlist", ManifestURL: "real-source-manifest", ProxyURL: "real-source-proxy", XtreamHost: "real-source-host", XtreamUsername: "real-source-user", XtreamPassword: "real-source-pass",
				EPG: config.EPGSettings{XmltvUrl: "real-source-xmltv", Sources: []config.EPGSource{{URL: "real-source-epg"}}},
			}},
			PlaylistSources: []config.LivePlaylistSource{{
				PlaylistURL: "real-legacy-playlist", ManifestURL: "real-legacy-manifest", ProxyURL: "real-legacy-proxy", XtreamHost: "real-legacy-host", XtreamUsername: "real-legacy-user", XtreamPassword: "real-legacy-pass",
				EPG: config.EPGSettings{XmltvUrl: "real-legacy-xmltv", Sources: []config.EPGSource{{URL: "real-legacy-epg"}}},
			}},
		},
	}
	encoded, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	var incoming config.Settings
	if err := json.Unmarshal(encoded, &incoming); err != nil {
		t.Fatal(err)
	}
	redactSettings(&incoming)

	preserveRedactedFields(&incoming, &existing)

	if incoming.MDBList.Accounts[0].APIKey != existing.MDBList.Accounts[0].APIKey {
		t.Fatal("MDBList account key was not restored")
	}
	if incoming.Scrob.Accounts[0].APIKey != existing.Scrob.Accounts[0].APIKey || incoming.Scrob.Accounts[0].Password != existing.Scrob.Accounts[0].Password || incoming.Scrob.Accounts[0].TOTPSecret != existing.Scrob.Accounts[0].TOTPSecret {
		t.Fatal("Scrob credentials were not restored")
	}
	if incoming.Live.PlaylistURL != existing.Live.PlaylistURL || incoming.Live.XtreamUsername != existing.Live.XtreamUsername || incoming.Live.EPG.Sources[0].URL != existing.Live.EPG.Sources[0].URL {
		t.Fatal("global live provider values were not restored")
	}
	if incoming.Live.Sources[0].ManifestURL != existing.Live.Sources[0].ManifestURL || incoming.Live.Sources[0].EPG.XmltvUrl != existing.Live.Sources[0].EPG.XmltvUrl {
		t.Fatal("live source values were not restored")
	}
	if incoming.Live.PlaylistSources[0].ProxyURL != existing.Live.PlaylistSources[0].ProxyURL || incoming.Live.PlaylistSources[0].EPG.Sources[0].URL != existing.Live.PlaylistSources[0].EPG.Sources[0].URL {
		t.Fatal("legacy live source values were not restored")
	}
}

func TestRedactedEffectivePlaylistURL(t *testing.T) {
	if got := redactedEffectivePlaylistURL(config.LiveSettings{PlaylistURL: "https://live.example/list?token=secret"}); got != redactedPlaceholder {
		t.Fatalf("playlist effective URL = %q", got)
	}
	if got := redactedEffectivePlaylistURL(config.LiveSettings{Mode: "xtream", XtreamHost: "https://live.example", XtreamUsername: "user", XtreamPassword: "pass"}); got != redactedPlaceholder {
		t.Fatalf("Xtream effective URL = %q", got)
	}
	if got := redactedEffectivePlaylistURL(config.LiveSettings{}); got != "" {
		t.Fatalf("empty effective URL = %q", got)
	}
}
