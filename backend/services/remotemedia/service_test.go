package remotemedia

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/jellyfin"
	"novastream/services/plex"
)

type fakeRemoteMediaRepo struct {
	libraries []models.RemoteMediaLibrary
	items     map[string][]models.RemoteMediaItem
}

func (f *fakeRemoteMediaRepo) ListLibraries(context.Context) ([]models.RemoteMediaLibrary, error) {
	return append([]models.RemoteMediaLibrary(nil), f.libraries...), nil
}
func (f *fakeRemoteMediaRepo) GetLibrary(context.Context, string) (*models.RemoteMediaLibrary, error) {
	return nil, nil
}
func (f *fakeRemoteMediaRepo) CreateLibrary(context.Context, *models.RemoteMediaLibrary) error {
	return nil
}
func (f *fakeRemoteMediaRepo) UpdateLibrary(context.Context, *models.RemoteMediaLibrary) error {
	return nil
}
func (f *fakeRemoteMediaRepo) DeleteLibrary(context.Context, string) error { return nil }
func (f *fakeRemoteMediaRepo) ListItems(_ context.Context, libraryID string, _ bool) ([]models.RemoteMediaItem, error) {
	return append([]models.RemoteMediaItem(nil), f.items[libraryID]...), nil
}
func (f *fakeRemoteMediaRepo) GetItem(context.Context, string) (*models.RemoteMediaItem, error) {
	return nil, nil
}
func (f *fakeRemoteMediaRepo) UpsertItem(context.Context, *models.RemoteMediaItem) error { return nil }
func (f *fakeRemoteMediaRepo) MarkItemsMissingNotSeenInSync(context.Context, string, string) error {
	return nil
}

func TestMatchesByExternalIDsIgnoresLocalizedTitle(t *testing.T) {
	group := models.LocalMediaItemGroup{
		Title:  "Zootropolis 2",
		Year:   2025,
		IMDBID: "tt26443597",
		TMDBID: 1084242,
		TVDBID: 344109,
	}
	if !matches(group, models.LocalMediaMatchQuery{
		Title:  "Zootopia 2",
		Year:   2025,
		IMDBID: "tt26443597",
		TMDBID: "1084242",
	}) {
		t.Fatal("expected IMDB/TMDB match despite localized Plex title")
	}
	if matches(group, models.LocalMediaMatchQuery{Title: "Zootopia 2", Year: 2025}) {
		t.Fatal("title-only query must not match a different localized title")
	}
}

func TestFindMatchesScansFullLibraryByExternalIDs(t *testing.T) {
	// Build enough A–Y filler so a title-sorted/paginated approach would drop "Zootropolis 2".
	filler := make([]models.RemoteMediaItem, 0, 220)
	for i := 0; i < 220; i++ {
		id := "filler-" + strconv.Itoa(i)
		filler = append(filler, models.RemoteMediaItem{
			ID:          id,
			LibraryID:   "plex-films",
			LibraryType: models.LocalMediaLibraryTypeMovie,
			Title:       "AAA Filler " + strconv.Itoa(i),
			Year:        2000,
			GroupKey:    id,
			FileName:    "filler.mkv",
		})
	}
	zootropolis := []models.RemoteMediaItem{
		{
			ID:           "plex-z2-4k",
			LibraryID:    "plex-films",
			LibraryType:  models.LocalMediaLibraryTypeMovie,
			Title:        "Zootropolis 2",
			Year:         2025,
			GroupKey:     "755091",
			FileName:     "Zootopia 2 2160p.mkv",
			VersionLabel: "2160p · HEVC",
			ExternalIDs:  &models.LocalMediaExternalIDs{IMDB: "tt26443597", TMDB: "1084242", TVDB: "344109"},
		},
		{
			ID:           "plex-z2-1080",
			LibraryID:    "plex-films",
			LibraryType:  models.LocalMediaLibraryTypeMovie,
			Title:        "Zootropolis 2",
			Year:         2025,
			GroupKey:     "755091",
			FileName:     "Zootopia 2 1080p.mkv",
			VersionLabel: "1080p · H264",
			ExternalIDs:  &models.LocalMediaExternalIDs{IMDB: "tt26443597", TMDB: "1084242", TVDB: "344109"},
		},
	}
	repo := &fakeRemoteMediaRepo{
		libraries: []models.RemoteMediaLibrary{{
			ID:       "plex-films",
			Name:     "Films",
			Type:     models.LocalMediaLibraryTypeMovie,
			Provider: models.MediaSourcePlex,
		}},
		items: map[string][]models.RemoteMediaItem{
			"plex-films": append(filler, zootropolis...),
		},
	}
	service := &Service{repo: repo}
	matches, err := service.FindMatches(context.Background(), models.LocalMediaMatchQuery{
		MediaType: "movie",
		Title:     "Zootopia 2",
		Year:      2025,
		IMDBID:    "tt26443597",
		TMDBID:    "1084242",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches=%d, want 1", len(matches))
	}
	if matches[0].LibraryName != "Films" || matches[0].Group.Title != "Zootropolis 2" {
		t.Fatalf("unexpected match: %+v", matches[0])
	}
	if len(matches[0].Group.Items) != 2 {
		t.Fatalf("items=%d, want 2 plex versions", len(matches[0].Group.Items))
	}
	if matches[0].Group.Items[0].SourceType != models.MediaSourcePlex {
		t.Fatalf("sourceType=%q, want plex", matches[0].Group.Items[0].SourceType)
	}
}

func TestLocalLibraryTypeMapsProviderLibraries(t *testing.T) {
	tests := map[string]models.LocalMediaLibraryType{
		"movie":      models.LocalMediaLibraryTypeMovie,
		"movies":     models.LocalMediaLibraryTypeMovie,
		"show":       models.LocalMediaLibraryTypeShow,
		"tvshows":    models.LocalMediaLibraryTypeShow,
		"artist":     models.LocalMediaLibraryTypeOther,
		"homevideos": models.LocalMediaLibraryTypeOther,
		"":           models.LocalMediaLibraryTypeOther,
	}
	for providerType, want := range tests {
		if got := localLibraryType(providerType); got != want {
			t.Errorf("localLibraryType(%q)=%q, want %q", providerType, got, want)
		}
	}
}

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

func TestPlexServerForLibraryPinsVerifiedAddress(t *testing.T) {
	now := time.Now()
	service := &Service{servers: plexServerResolver{entries: map[string]plexServerCacheEntry{
		"account-1\x00server-1": {
			server: plex.PlexResource{
				ClientIdentifier: "server-1",
				Connections:      []plex.PlexConnection{{Protocol: "http", URI: "http://192.0.2.1:32400", Local: true}},
			},
			authToken: "token-1",
			expiresAt: now.Add(time.Minute),
		},
	}, now: func() time.Time { return now }}}
	library := &models.RemoteMediaLibrary{
		AccountID: "account-1",
		ServerID:  "server-1",
		ServerURL: "http://100.64.0.10:32400",
	}

	server, err := service.plexServerForLibrary(library, "token-1")
	if err != nil {
		t.Fatalf("plexServerForLibrary() error = %v", err)
	}
	got, err := plex.PreferredConnection(server)
	if err != nil {
		t.Fatal(err)
	}
	if got != library.ServerURL {
		t.Fatalf("preferred connection=%q, want verified address %q", got, library.ServerURL)
	}
}

func TestNormalizeJellyfinEpisodeVersions(t *testing.T) {
	dateCreated := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	library := &models.RemoteMediaLibrary{ID: "lib", Type: models.LocalMediaLibraryTypeShow, Provider: models.MediaSourceJellyfin}
	items := normalizeJellyfin(library, []jellyfin.JellyfinItem{{
		ID: "episode-1", Name: "Pilot", Type: "Episode", SeriesID: "series-1", SeriesName: "Example Show",
		SeasonNum: 1, EpisodeNum: 1, DateCreated: &dateCreated, ProviderIDs: map[string]string{"tvdb": "42"},
		MediaSources: []jellyfin.JellyfinMediaSource{{ID: "source-4k", Path: "/media/pilot.mkv", Container: "mkv", Size: 100, RunTimeTicks: 1_205_000_000,
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
	if item.DurationSeconds != 120.5 {
		t.Fatalf("DurationSeconds=%v, want 120.5", item.DurationSeconds)
	}
	if !item.CreatedAt.Equal(dateCreated) {
		t.Fatalf("CreatedAt=%v, want Jellyfin DateCreated %v", item.CreatedAt, dateCreated)
	}
}

func TestNormalizePlexMovieParts(t *testing.T) {
	addedAt := time.Date(2023, 4, 5, 6, 7, 8, 0, time.UTC)
	library := &models.RemoteMediaLibrary{ID: "lib", Type: models.LocalMediaLibraryTypeMovie, Provider: models.MediaSourcePlex}
	items := normalizePlex(library, []plex.PlexLibraryItem{{RatingKey: "10", Title: "Example Movie", Type: "movie", Year: 2025, Duration: 120500, AddedAt: addedAt.Unix(),
		Guid: []plex.PlexGuid{{ID: "tmdb://123"}}, Media: []plex.PlexMedia{{VideoCodec: "h264", Height: 1080,
			Part: []plex.PlexPart{{ID: 7, Key: "/library/parts/7/file.mkv", File: "/movies/file.mkv", Size: 99}}}},
	}})
	if len(items) != 1 {
		t.Fatalf("len(items)=%d, want 1", len(items))
	}
	if items[0].ExternalIDs.TMDB != "123" || items[0].ProviderData["partKey"] == "" {
		t.Fatalf("unexpected Plex normalization: %#v", items[0])
	}
	if items[0].DurationSeconds != 120.5 {
		t.Fatalf("DurationSeconds=%v, want 120.5", items[0].DurationSeconds)
	}
	if !items[0].CreatedAt.Equal(addedAt) {
		t.Fatalf("CreatedAt=%v, want Plex AddedAt %v", items[0].CreatedAt, addedAt)
	}
}

func TestRemoteMediaItemProviderDataSurvivesJSONRoundTrip(t *testing.T) {
	// Backup/restore must keep partKey; previously json:"-" dropped it and broke Plex playback.
	item := models.RemoteMediaItem{
		ID: "ri1", LibraryID: "rm1", ExternalItemID: "10", ExternalMediaID: "7",
		Title: "Example", StreamPath: "plexmedia:ri1",
		ProviderData: map[string]string{"partKey": "/library/parts/7/file.mkv", "posterPath": "/thumb"},
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored models.RemoteMediaItem
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.ProviderData["partKey"] != "/library/parts/7/file.mkv" {
		t.Fatalf("providerData lost in JSON round-trip: %#v (raw=%s)", restored.ProviderData, raw)
	}
}

func TestSortRemoteMediaGroupsByDateAdded(t *testing.T) {
	oldest := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	newest := oldest.Add(24 * time.Hour)
	groups := []models.LocalMediaItemGroup{
		{ID: "old", Title: "Alpha", LatestCreatedAt: &oldest},
		{ID: "new", Title: "Zulu", LatestCreatedAt: &newest},
	}

	sortRemoteMediaGroups(groups, "created", "desc")
	if groups[0].ID != "new" {
		t.Fatalf("descending first group=%q, want new", groups[0].ID)
	}
	sortRemoteMediaGroups(groups, "created", "asc")
	if groups[0].ID != "old" {
		t.Fatalf("ascending first group=%q, want old", groups[0].ID)
	}
}

func TestRemotePlaybackSourceAndState(t *testing.T) {
	tests := []struct {
		path     string
		provider string
		itemID   string
	}{
		{"plexmedia:item-1", models.MediaSourcePlex, "item-1"},
		{"jellyfinmedia:item-2/stream", models.MediaSourceJellyfin, "item-2"},
		{"/movies/file.mkv", "", ""},
	}
	for _, tc := range tests {
		provider, itemID := remotePlaybackSource(tc.path)
		if provider != tc.provider || itemID != tc.itemID {
			t.Fatalf("remotePlaybackSource(%q)=(%q,%q), want (%q,%q)", tc.path, provider, itemID, tc.provider, tc.itemID)
		}
	}

	if got := remotePlaybackState(models.PlaybackProgressUpdate{}, false); got != "playing" {
		t.Fatalf("playing state=%q", got)
	}
	if got := remotePlaybackState(models.PlaybackProgressUpdate{IsBuffering: true, IsPaused: true}, false); got != "buffering" {
		t.Fatalf("buffering state=%q", got)
	}
	if got := remotePlaybackState(models.PlaybackProgressUpdate{IsPaused: true}, false); got != "paused" {
		t.Fatalf("paused state=%q", got)
	}
	if got := remotePlaybackState(models.PlaybackProgressUpdate{PlaybackEnded: true}, false); got != "stopped" {
		t.Fatalf("ended state=%q", got)
	}
}

func TestRemoteCatalogTitleID(t *testing.T) {
	if got := remoteCatalogTitleID(&models.RemoteMediaItem{
		GroupKey:    "264995",
		LibraryType: models.LocalMediaLibraryTypeMovie,
		ExternalIDs: &models.LocalMediaExternalIDs{},
	}); got != "" {
		t.Fatalf("untagged movie titleID=%q, want empty", got)
	}
	if got := remoteCatalogTitleID(&models.RemoteMediaItem{
		GroupKey:    "264995",
		LibraryType: models.LocalMediaLibraryTypeMovie,
		ExternalIDs: &models.LocalMediaExternalIDs{TMDB: "550", IMDB: "tt0137523"},
	}); got != "tmdb:movie:550" {
		t.Fatalf("matched movie titleID=%q, want tmdb:movie:550", got)
	}
	if got := remoteCatalogTitleID(&models.RemoteMediaItem{
		GroupKey:    "series-1",
		LibraryType: models.LocalMediaLibraryTypeShow,
		ExternalIDs: &models.LocalMediaExternalIDs{TVDB: "121361"},
	}); got != "tvdb:series:121361" {
		t.Fatalf("matched show titleID=%q, want tvdb:series:121361", got)
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
