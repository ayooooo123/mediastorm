package remotemedia

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/jellyfin"
	"novastream/services/plex"
)

func TestPlexServerResolverCollapsesConcurrentLoads(t *testing.T) {
	resolver := &plexServerResolver{}
	var loads atomic.Int32
	load := func(string) ([]plex.PlexResource, error) {
		loads.Add(1)
		time.Sleep(10 * time.Millisecond)
		return []plex.PlexResource{{ClientIdentifier: "server-1", Name: "Den"}}, nil
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server, err := resolver.resolve("account-1", "token-1", "server-1", load)
			if err != nil || server.ClientIdentifier != "server-1" {
				t.Errorf("resolve() server=%#v err=%v", server, err)
			}
		}()
	}
	wg.Wait()

	if got := loads.Load(); got != 1 {
		t.Fatalf("resource loads=%d, want 1", got)
	}
}

func TestPlexServerResolverRefreshesForChangedTokenAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	resolver := &plexServerResolver{now: func() time.Time { return now }}
	loads := 0
	load := func(string) ([]plex.PlexResource, error) {
		loads++
		return []plex.PlexResource{{ClientIdentifier: "server-1"}}, nil
	}

	for _, token := range []string{"token-1", "token-1", "token-2"} {
		if _, err := resolver.resolve("account-1", token, "server-1", load); err != nil {
			t.Fatal(err)
		}
	}
	if loads != 2 {
		t.Fatalf("loads after token change=%d, want 2", loads)
	}

	now = now.Add(plexServerCacheTTL)
	if _, err := resolver.resolve("account-1", "token-2", "server-1", load); err != nil {
		t.Fatal(err)
	}
	if loads != 3 {
		t.Fatalf("loads after expiry=%d, want 3", loads)
	}
}

func TestNormalizeJellyfinEpisodeVersions(t *testing.T) {
	library := &models.RemoteMediaLibrary{ID: "lib", Type: models.LocalMediaLibraryTypeShow, Provider: models.MediaSourceJellyfin}
	items := normalizeJellyfin(library, []jellyfin.JellyfinItem{{
		ID: "episode-1", Name: "Pilot", Type: "Episode", SeriesID: "series-1", SeriesName: "Example Show",
		SeasonNum: 1, EpisodeNum: 1, ProviderIDs: map[string]string{"tvdb": "42"},
		MediaSources: []jellyfin.JellyfinMediaSource{{ID: "source-4k", Path: "/media/pilot.mkv", Container: "mkv", Size: 100,
			MediaStreams: []jellyfin.JellyfinMediaStream{{Type: "Video", Codec: "hevc", Width: 3840, Height: 2160, VideoRange: "HDR10"}}}},
	}})
	if len(items) != 1 {
		t.Fatalf("len(items)=%d, want 1", len(items))
	}
	item := items[0]
	if item.GroupKey != "series-1" || item.Title != "Example Show" || item.EpisodeTitle != "Pilot" {
		t.Fatalf("unexpected normalized item: %#v", item)
	}
	if item.VersionLabel != "2160p · HEVC · HDR10" {
		t.Fatalf("VersionLabel=%q", item.VersionLabel)
	}
	if item.ProviderData["mediaSourceId"] != "source-4k" {
		t.Fatalf("missing media source ID")
	}
}

func TestNormalizePlexMovieParts(t *testing.T) {
	library := &models.RemoteMediaLibrary{ID: "lib", Type: models.LocalMediaLibraryTypeMovie, Provider: models.MediaSourcePlex}
	items := normalizePlex(library, []plex.PlexLibraryItem{{RatingKey: "10", Title: "Example Movie", Type: "movie", Year: 2025,
		Guid: []plex.PlexGuid{{ID: "tmdb://123"}}, Media: []plex.PlexMedia{{VideoCodec: "h264", Height: 1080,
			Part: []plex.PlexPart{{ID: 7, Key: "/library/parts/7/file.mkv", File: "/movies/file.mkv", Size: 99}}}},
	}})
	if len(items) != 1 {
		t.Fatalf("len(items)=%d, want 1", len(items))
	}
	if items[0].ExternalIDs.TMDB != "123" || items[0].ProviderData["partKey"] == "" {
		t.Fatalf("unexpected Plex normalization: %#v", items[0])
	}
}

func TestGroupItemsPreservesEpisodeVersionsAndSource(t *testing.T) {
	library := &models.RemoteMediaLibrary{ID: "lib", Name: "TV", Type: models.LocalMediaLibraryTypeShow, Provider: models.MediaSourceJellyfin, ServerName: "Den"}
	items := []models.RemoteMediaItem{
		{ID: "v1", LibraryID: "lib", GroupKey: "show", LibraryType: models.LocalMediaLibraryTypeShow, Title: "Show", SeasonNumber: 1, EpisodeNumber: 2, EpisodeTitle: "Two", VersionLabel: "1080p"},
		{ID: "v2", LibraryID: "lib", GroupKey: "show", LibraryType: models.LocalMediaLibraryTypeShow, Title: "Show", SeasonNumber: 1, EpisodeNumber: 2, EpisodeTitle: "Two", VersionLabel: "4K"},
	}
	groups := groupItems(library, items, false)
	if len(groups) != 1 || len(groups[0].Seasons) != 1 || len(groups[0].Seasons[0].Episodes[0].Items) != 2 {
		t.Fatalf("versions were not grouped: %#v", groups)
	}
	if groups[0].Seasons[0].Episodes[0].Items[0].SourceType != models.MediaSourceJellyfin {
		t.Fatalf("source tag missing")
	}
}
