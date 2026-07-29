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
}

func (f *fakeSharedShelfHistory) AggregatePopularTitles(eligible map[string]bool, _ int) []models.PopularTitle {
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

func (f *fakeSharedShelfHistory) ListRecentWatches(eligible map[string]models.User, _ int, _ int) []models.RecentWatch {
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
	handler := NewMetadataHandler(&fakeMetadataService{}, nil)
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
}
