package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/models"
)

type fakeSharedShelfHistory struct {
	*fakeMetadataHistoryService
	popularWindowDays   int
	popularMinProfiles  int
	recentWindowDays    int
	recentMaxPerProfile int
}

func (f *fakeSharedShelfHistory) AggregatePopularTitles(
	eligible map[string]bool,
	windowDays, minProfiles int,
) []models.PopularTitle {
	f.popularWindowDays = windowDays
	f.popularMinProfiles = minProfiles
	if !eligible["shared"] {
		return nil
	}
	return []models.PopularTitle{{
		MediaType:  "movie",
		ItemID:     "tmdb:10",
		Name:       "Shared Movie",
		WatchCount: 1,
	}}
}

func (f *fakeSharedShelfHistory) ListRecentWatches(
	eligible map[string]models.User,
	windowDays, maxPerProfile int,
) []models.RecentWatch {
	f.recentWindowDays = windowDays
	f.recentMaxPerProfile = maxPerProfile
	user, ok := eligible["shared"]
	if !ok {
		return nil
	}
	return []models.RecentWatch{{
		UserName:      user.Name,
		MediaType:     "episode",
		ItemID:        "tvdb:20:s01e02",
		Name:          "Second Episode",
		SeriesID:      "tvdb:20",
		SeriesName:    "Shared Show",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		WatchedAt:     time.Now().UTC(),
	}}
}

func TestSharedShelfQuerySettingsReachAggregation(t *testing.T) {
	history := &fakeSharedShelfHistory{fakeMetadataHistoryService: &fakeMetadataHistoryService{}}
	handler := NewMetadataHandler(&fakeMetadataService{}, nil)
	handler.SetHistoryService(history)
	handler.SetUsersService(&fakeSharedShelfUsers{users: []models.User{{
		ID: "shared", Name: "Watcher", ActivityPrivacy: models.ActivityPrivacyShared,
	}}})

	popularResponse := httptest.NewRecorder()
	handler.PopularOnServer(popularResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/discover/popular-on-server?activityWindowDays=30&minimumProfiles=3",
		nil,
	))
	if history.popularWindowDays != 30 || history.popularMinProfiles != 3 {
		t.Fatalf("popular settings = %d days/%d minimum views", history.popularWindowDays, history.popularMinProfiles)
	}
	var popularPayload PopularOnServerResponse
	if err := json.NewDecoder(popularResponse.Body).Decode(&popularPayload); err != nil {
		t.Fatalf("decode popular response: %v", err)
	}
	if len(popularPayload.Items) != 1 || popularPayload.Items[0].Title.CardSubtitle != "1 view" {
		t.Fatalf("popular view context missing from card: %+v", popularPayload.Items)
	}

	recentResponse := httptest.NewRecorder()
	handler.RecentlyWatched(recentResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/discover/recently-watched?activityWindowDays=7&maxItemsPerProfile=5",
		nil,
	))
	if history.recentWindowDays != 7 || history.recentMaxPerProfile != 5 {
		t.Fatalf("recent settings = %d days/%d items", history.recentWindowDays, history.recentMaxPerProfile)
	}
}

type fakeSharedShelfUsers struct {
	users []models.User
}

func (f *fakeSharedShelfUsers) Get(id string) (models.User, bool) {
	for _, user := range f.users {
		if user.ID == id {
			return user, true
		}
	}
	return models.User{}, false
}

func (f *fakeSharedShelfUsers) ListAll() []models.User {
	return append([]models.User(nil), f.users...)
}

func TestPopularOnServerRequiresExplicitProfileOptIn(t *testing.T) {
	users := &fakeSharedShelfUsers{users: []models.User{{
		ID: "shared", Name: "Watcher", ActivityPrivacy: models.ActivityPrivacyNotShared,
	}}}
	handler := NewMetadataHandler(&fakeMetadataService{}, nil)
	handler.SetHistoryService(&fakeSharedShelfHistory{fakeMetadataHistoryService: &fakeMetadataHistoryService{}})
	handler.SetUsersService(users)

	request := httptest.NewRequest(http.MethodGet, "/api/discover/popular-on-server", nil)
	privateResponse := httptest.NewRecorder()
	handler.PopularOnServer(privateResponse, request)
	var privatePayload PopularOnServerResponse
	if err := json.NewDecoder(privateResponse.Body).Decode(&privatePayload); err != nil {
		t.Fatalf("decode private response: %v", err)
	}
	if privatePayload.Total != 0 || len(privatePayload.Items) != 0 {
		t.Fatalf("private profile contributed activity: %+v", privatePayload)
	}

	users.users[0].ActivityPrivacy = models.ActivityPrivacyShared
	sharedResponse := httptest.NewRecorder()
	handler.PopularOnServer(sharedResponse, request)
	var sharedPayload PopularOnServerResponse
	if err := json.NewDecoder(sharedResponse.Body).Decode(&sharedPayload); err != nil {
		t.Fatalf("decode shared response: %v", err)
	}
	if sharedPayload.Total != 1 || len(sharedPayload.Items) != 1 {
		t.Fatalf("opted-in profile missing from response: %+v", sharedPayload)
	}
}

func TestRecentlyWatchedUsesTrendingItemContract(t *testing.T) {
	episodeImage := &models.Image{URL: "https://example.com/episode.jpg", Type: "backdrop"}
	handler := NewMetadataHandler(&fakeMetadataService{seriesResp: &models.SeriesDetails{
		Title: models.Title{ID: "tvdb:20", Name: "Shared Show", MediaType: "series"},
		Seasons: []models.SeriesSeason{{
			Number: 1,
			Episodes: []models.SeriesEpisode{{
				SeasonNumber: 1, EpisodeNumber: 2, Image: episodeImage,
			}},
		}},
	}}, nil)
	handler.SetHistoryService(&fakeSharedShelfHistory{fakeMetadataHistoryService: &fakeMetadataHistoryService{}})
	handler.SetUsersService(&fakeSharedShelfUsers{users: []models.User{{
		ID: "shared", Name: "Watcher", ActivityPrivacy: models.ActivityPrivacyShared,
	}}})

	response := httptest.NewRecorder()
	handler.RecentlyWatched(response, httptest.NewRequest(http.MethodGet, "/api/discover/recently-watched", nil))

	var payload RecentlyWatchedResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 {
		t.Fatalf("unexpected response: %+v", payload)
	}
	title := payload.Items[0].Title
	if title.ID != "tvdb:20" || title.MediaType != "series" || title.Name != "Shared Show" {
		t.Fatalf("recent episode was not normalized to its series card: %+v", title)
	}
	if title.CardSubtitle != "Watcher watched S01E02 · Second Episode" || !title.ForceTitleOverlay {
		t.Fatalf("recent-watch context missing from card: %+v", title)
	}
	if title.CardImage == nil || title.CardImage.URL != episodeImage.URL {
		t.Fatalf("recent episode image missing from landscape card: %+v", title.CardImage)
	}
}
