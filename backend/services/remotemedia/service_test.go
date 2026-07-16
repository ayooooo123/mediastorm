package remotemedia

import (
	"testing"

	"novastream/models"
	"novastream/services/jellyfin"
	"novastream/services/plex"
)

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
