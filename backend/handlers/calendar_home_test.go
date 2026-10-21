package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novastream/handlers"
	"novastream/models"
	"novastream/services/calendar"

	"github.com/gorilla/mux"
)

type slowCalendarWatchlist struct {
	delay time.Duration
	items []models.WatchlistItem
}

func (m *slowCalendarWatchlist) List(_ string) ([]models.WatchlistItem, error) {
	time.Sleep(m.delay)
	return m.items, nil
}

func homeCalendarRequest(t *testing.T, h *handlers.CalendarHandler, query string) (models.CalendarResponse, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar?"+query, nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()
	h.GetCalendar(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	var resp models.CalendarResponse
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{}
	_ = json.Unmarshal([]byte(body), &raw)
	return resp, raw
}

func TestGetCalendarHomeReturnsPendingWithoutBlocking(t *testing.T) {
	svc := calendar.New(
		&calendarMockMetadataWithData{series: map[int64]*models.SeriesDetails{
			100: {
				Title: models.Title{Name: "Show", TVDBID: 100},
				Seasons: []models.SeriesSeason{{Number: 1, Episodes: []models.SeriesEpisode{
					{Name: "Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02")},
				}}},
			},
		}},
		&slowCalendarWatchlist{delay: 400 * time.Millisecond, items: []models.WatchlistItem{
			{ID: "tvdb:100", MediaType: "series", Name: "Show", ExternalIDs: map[string]string{"tvdb": "100"}},
		}},
		&calendarMockHistory{},
		&calendarMockUserSettings{},
		&calendarMockUsers{},
	)
	h := handlers.NewCalendarHandler(svc, &mockCalendarUserService{exists: map[string]bool{"user1": true}}, false)

	start := time.Now()
	resp, raw := homeCalendarRequest(t, h, "home=1&compact=1&limit=10&days=14&recentDays=90")
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("home calendar blocked on the build for %s", elapsed)
	}
	if !resp.Pending || len(resp.Items) != 0 || resp.Total != 0 {
		t.Fatalf("expected an empty pending response, got %+v", resp)
	}
	if items, ok := raw["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("items must encode as an empty array, got %v", raw["items"])
	}

	// The kicked background build fills the cache; later polls get items.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, raw = homeCalendarRequest(t, h, "home=1&compact=1&limit=10&days=14&recentDays=90")
		if len(resp.Items) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background build never populated the home calendar")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp.Pending {
		t.Fatal("pending must be cleared once a calendar is served")
	}
	if _, present := raw["pending"]; present {
		t.Fatal("pending should be omitted when false")
	}
	if resp.Items[0].Title != "Show" || resp.RefreshedAt == "" {
		t.Fatalf("unexpected served calendar: %+v", resp)
	}
}

func TestGetCalendarNonHomeStillBuildsSynchronously(t *testing.T) {
	svc := calendar.New(
		&calendarMockMetadataWithData{series: map[int64]*models.SeriesDetails{
			100: {
				Title: models.Title{Name: "Show", TVDBID: 100},
				Seasons: []models.SeriesSeason{{Number: 1, Episodes: []models.SeriesEpisode{
					{Name: "Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02")},
				}}},
			},
		}},
		&slowCalendarWatchlist{delay: 50 * time.Millisecond, items: []models.WatchlistItem{
			{ID: "tvdb:100", MediaType: "series", Name: "Show", ExternalIDs: map[string]string{"tvdb": "100"}},
		}},
		&calendarMockHistory{},
		&calendarMockUserSettings{},
		&calendarMockUsers{},
	)
	h := handlers.NewCalendarHandler(svc, &mockCalendarUserService{exists: map[string]bool{"user1": true}}, false)

	resp, _ := homeCalendarRequest(t, h, "days=14")
	if resp.Pending || len(resp.Items) != 1 {
		t.Fatalf("Calendar tab request should block and return the built calendar, got %+v", resp)
	}
	// Home now serves the cached calendar with the same windowing.
	resp, _ = homeCalendarRequest(t, h, "home=1&limit=5&tz=America/New_York")
	if resp.Pending || len(resp.Items) != 1 || resp.Timezone != "America/New_York" {
		t.Fatalf("expected cached home calendar, got %+v", resp)
	}
}
