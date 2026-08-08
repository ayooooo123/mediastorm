package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

type homepageStreamsStub struct {
	response StreamsResponse
}

func (s homepageStreamsStub) ActiveStreams() StreamsResponse {
	return s.response
}

type homepageUsersStub struct {
	users []models.User
}

func (s homepageUsersStub) ListAll() []models.User {
	return s.users
}

func TestHomepageUsesCanonicalDashboardStreams(t *testing.T) {
	createdAt := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	handler := NewHomepageHandler(nil)
	handler.SetAPIKey("homepage-secret")
	handler.SetStreamsProvider(homepageStreamsStub{response: StreamsResponse{
		Count: 2,
		Streams: []StreamInfo{
			{
				ID:              "stream-nazara",
				Type:            "direct",
				Filename:        "movie.mkv",
				ProfileName:     "nazara",
				CreatedAt:       createdAt,
				CurrentPosition: 120,
				PercentWatched:  10,
			},
			{
				ID:          "stream-mom",
				Type:        "hls",
				Filename:    "show.s01e01.mkv",
				ProfileName: "mom",
				CreatedAt:   createdAt,
				HasHDR:      true,
			},
		},
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/homepage", nil)
	req.Header.Set("X-API-Key", "homepage-secret")
	rec := httptest.NewRecorder()
	handler.GetStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var stats HomepageStats
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if stats.ActiveStreams != 2 || len(stats.Streams) != 2 {
		t.Fatalf("activeStreams/streams = %d/%d, want 2/2", stats.ActiveStreams, len(stats.Streams))
	}
	if got := stats.Streams[0]; got.ProfileName != "nazara" || got.Type != "direct" || got.CurrentPosition != 120 {
		t.Errorf("first stream = %+v, want canonical nazara direct stream", got)
	}
	if got := stats.Streams[1]; got.ProfileName != "mom" || got.Type != "hls" || !got.HasHDR {
		t.Errorf("second stream = %+v, want canonical mom HLS stream", got)
	}
}

func TestDashboardShelfUsesPresentationSafeCanonicalStreams(t *testing.T) {
	createdAt := time.Date(2026, 7, 29, 18, 30, 0, 0, time.UTC)
	handler := NewHomepageHandler(nil)
	handler.SetUserService(homepageUsersStub{users: []models.User{
		{ID: "profile-liam", Name: "Liam", ActivityPrivacy: models.ActivityPrivacyShared},
		{ID: "profile-guest", Name: "Guest", ActivityPrivacy: models.ActivityPrivacySharedAnonymous},
		{ID: "profile-private", Name: "Private", ActivityPrivacy: models.ActivityPrivacyNotShared},
	}})
	handler.SetStreamsProvider(homepageStreamsStub{response: StreamsResponse{
		Count: 2,
		Streams: []StreamInfo{
			{
				ID:              "stream-paused",
				ItemID:          "tmdb:tv:123:s1:e2",
				Path:            "/secret/media/show.mkv",
				ClientIP:        "10.0.0.5",
				UserAgent:       "private-player-agent",
				ProfileIDs:      []string{"profile-liam", "profile-guest", "profile-private"},
				ProfileNames:    []string{"Liam", "Guest", "Private"},
				CreatedAt:       createdAt,
				Duration:        2400,
				CurrentPosition: 600,
				PercentWatched:  25,
				IsPaused:        true,
				MediaType:       "episode",
				Title:           "Example Show",
				SeasonNumber:    1,
				EpisodeNumber:   2,
				EpisodeName:     "Second Episode",
				PosterURL:       "https://images.example/poster.jpg",
			},
			{
				ID:            "stream-private",
				ProfileID:     "profile-private",
				ProfileName:   "Private",
				CreatedAt:     createdAt,
				MediaType:     "movie",
				Title:         "Private Movie",
				PosterURL:     "https://images.example/private.jpg",
				IsPaused:      false,
				ExternalIDs:   map[string]string{"tmdb": "456"},
				BytesStreamed: 1,
			},
			{
				ID:              "stream-live",
				ProfileID:       "profile-liam",
				ProfileName:     "Liam",
				CreatedAt:       createdAt,
				MediaType:       "channel",
				ItemID:          "news-1",
				Title:           "News One",
				LiveSourceURL:   "https://iptv.example/news.m3u8",
				LiveSourceID:    "provider-1",
				LiveChannelLogo: "https://images.example/news.png",
				BytesStreamed:   1,
			},
		},
	}})

	rec := httptest.NewRecorder()
	handler.GetDashboardShelf(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard/shelf", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	var response DashboardShelfResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Count != 2 || len(response.Streams) != 2 {
		t.Fatalf("count/streams = %d/%d, want 2/2", response.Count, len(response.Streams))
	}
	stream := response.Streams[0]
	if !stream.IsPaused || stream.Status != "paused" || stream.PercentWatched != 25 {
		t.Fatalf("unexpected playback state: %+v", stream)
	}
	if len(stream.ProfileNames) != 2 || stream.ProfileNames[0] != "Liam" || stream.ProfileNames[1] != "Fellow user" {
		t.Fatalf("unexpected watcher names: %+v", stream.ProfileNames)
	}
	if strings.Contains(body, "Private") {
		t.Fatalf("dashboard shelf response included a non-sharing profile: %s", body)
	}
	for _, sensitive := range []string{"/secret/media/show.mkv", "10.0.0.5", "private-player-agent"} {
		if strings.Contains(body, sensitive) {
			t.Fatalf("dashboard shelf response leaked %q", sensitive)
		}
	}
	live := response.Streams[1]
	if live.MediaType != "channel" || live.LiveSourceURL != "https://iptv.example/news.m3u8" || live.LiveChannelLogo != "https://images.example/news.png" {
		t.Fatalf("live dashboard stream missing channel playback metadata: %+v", live)
	}
}

func TestFindMatchingProgress_EpisodePreciseMatch(t *testing.T) {
	// When filename contains S##E## pattern, the precise match should win
	// even if other episodes of the same series are in the list.
	progressList := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  3,
			EpisodeNumber: 11,
			Position:      56,
			Duration:      1517,
			UpdatedAt:     time.Date(2026, 1, 11, 19, 58, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  2,
			EpisodeNumber: 2,
			Position:      1000,
			Duration:      1491,
			UpdatedAt:     time.Date(2026, 1, 24, 21, 5, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  1,
			EpisodeNumber: 7,
			Position:      237,
			Duration:      1477,
			UpdatedAt:     time.Date(2026, 2, 23, 19, 53, 0, 0, time.UTC),
		},
	}

	filename := "Record.of.Ragnarok.S01E07.1080p.WEB.H264-SUGOI.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a match, got nil")
	}
	if match.SeasonNumber != 1 || match.EpisodeNumber != 7 {
		t.Errorf("expected S01E07, got S%02dE%02d", match.SeasonNumber, match.EpisodeNumber)
	}
}

func TestFindMatchingProgress_NameOnlyFallbackPicksMostRecent(t *testing.T) {
	// When filename does NOT contain S##E## (e.g. debrid URL without episode in name),
	// the name-only fallback should pick the most recently updated entry.
	progressList := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  3,
			EpisodeNumber: 11,
			Position:      56,
			Duration:      1517,
			UpdatedAt:     time.Date(2026, 1, 11, 19, 58, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  1,
			EpisodeNumber: 7,
			Position:      237,
			Duration:      1477,
			UpdatedAt:     time.Date(2026, 2, 23, 19, 53, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  2,
			EpisodeNumber: 2,
			Position:      1000,
			Duration:      1491,
			UpdatedAt:     time.Date(2026, 1, 24, 21, 5, 0, 0, time.UTC),
		},
	}

	// Filename has series name but no S##E## pattern
	filename := "Record.of.Ragnarok.1080p.WEB.H264-SUGOI.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a match via name-only fallback, got nil")
	}
	// Should pick S1E07 because it has the most recent UpdatedAt
	if match.SeasonNumber != 1 || match.EpisodeNumber != 7 {
		t.Errorf("expected S01E07 (most recent), got S%02dE%02d", match.SeasonNumber, match.EpisodeNumber)
	}
}

func TestFindMatchingProgress_PreciseMatchBeatsNameOnly(t *testing.T) {
	// When filename has S##E##, precise match should win even if a different
	// episode has a more recent UpdatedAt.
	progressList := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  1,
			EpisodeNumber: 7,
			Position:      237,
			Duration:      1477,
			UpdatedAt:     time.Date(2026, 2, 23, 19, 53, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  3,
			EpisodeNumber: 12,
			Position:      500,
			Duration:      1500,
			// More recent than S1E07
			UpdatedAt: time.Date(2026, 2, 24, 12, 0, 0, 0, time.UTC),
		},
	}

	filename := "Record.of.Ragnarok.S01E07.1080p.WEB.H264-SUGOI.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a match, got nil")
	}
	// Should pick S1E07 via precise match, not S3E12 despite more recent timestamp
	if match.SeasonNumber != 1 || match.EpisodeNumber != 7 {
		t.Errorf("expected S01E07 (precise match), got S%02dE%02d", match.SeasonNumber, match.EpisodeNumber)
	}
}

func TestFindMatchingProgress_MovieNameMatch(t *testing.T) {
	// Movies should match on name only (no season/episode constraint).
	progressList := []models.PlaybackProgress{
		{
			MediaType: "movie",
			MovieName: "Free Guy",
			Position:  2586,
			Duration:  6898,
		},
	}

	filename := "Free.Guy.2021.1080p.BluRay.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a movie match, got nil")
	}
	if match.MovieName != "Free Guy" {
		t.Errorf("expected Free Guy, got %s", match.MovieName)
	}
}
