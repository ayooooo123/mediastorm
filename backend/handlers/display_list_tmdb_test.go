package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/handlers"
	"novastream/models"
	metadatapkg "novastream/services/metadata"

	"github.com/gorilla/mux"
)

type displayListTMDBMetadata struct {
	*mockMetadataServiceStartup
	options metadatapkg.TMDBListOptions
	items   []models.TrendingItem
	total   int
}

func (m *displayListTMDBMetadata) GetTMDBList(_ context.Context, options metadatapkg.TMDBListOptions) ([]models.TrendingItem, int, error) {
	m.options = options
	if m.items != nil {
		start := min(options.Offset, len(m.items))
		end := len(m.items)
		if options.Limit > 0 {
			end = min(end, start+options.Limit)
		}
		return append([]models.TrendingItem(nil), m.items[start:end]...), m.total, nil
	}
	return []models.TrendingItem{{Title: models.Title{ID: "tmdb:movie:11", Name: "Star Wars", MediaType: "movie", Status: models.MovieReleaseStatusReleased}}}, 1, nil
}

func TestDisplayListRoutesWatchTMDBSentinelToTMDBShelf(t *testing.T) {
	cfgManager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfgManager.Save(config.Settings{
		HomeShelves: config.HomeShelvesSettings{
			Shelves: []config.ShelfConfig{
				{
					ID:                "tmdb-production-company-2",
					Name:              "Walt Disney Pictures",
					Enabled:           true,
					Type:              "tmdb",
					TMDBSourceType:    "production-company",
					TMDBSourceID:      "2",
					TMDBMediaType:     "movie",
					TMDBDiscoverQuery: "genres=16",
					Sort:              "popularity.desc",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	metadataService := &displayListTMDBMetadata{mockMetadataServiceStartup: &mockMetadataServiceStartup{}}
	metadataHandler := handlers.NewMetadataHandler(metadataService, cfgManager)
	displayListHandler := handlers.NewDisplayListHandler(nil, nil, &mockUserServiceStartup{exists: true})
	displayListHandler.SetMetadataHandler(metadataHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/display-list?source=mdblist&url=mediastorm%3Atmdb%3Atmdb-production-company-2&limit=50&offset=0&includeFacets=true", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()
	displayListHandler.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Source string                `json:"source"`
		Items  []models.TrendingItem `json:"items"`
		Total  int                   `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Source != "tmdb-list" || response.Total != 1 || len(response.Items) != 1 || response.Items[0].Title.Name != "Star Wars" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if got := metadataService.options; got.SourceType != "production-company" || got.SourceID != "2" ||
		got.MediaType != "movie" || got.Sort != "popularity.desc" || got.DiscoverQuery != "genres=16" || got.Limit != 50 {
		t.Fatalf("unexpected TMDB options: %+v", got)
	}

	nextReq := httptest.NewRequest(http.MethodGet, "/api/users/user1/display-list?source=mdblist&url=mediastorm%3Atmdb%3Atmdb-production-company-2&limit=50&offset=50&includeFacets=true", nil)
	nextReq = mux.SetURLVars(nextReq, map[string]string{"userID": "user1"})
	nextRec := httptest.NewRecorder()
	displayListHandler.Get(nextRec, nextReq)
	if nextRec.Code != http.StatusOK {
		t.Fatalf("next page: expected 200, got %d: %s", nextRec.Code, nextRec.Body.String())
	}
	if got := metadataService.options; got.Limit != 50 || got.Offset != 50 {
		t.Fatalf("next page pagination = limit %d offset %d, want 50/50", got.Limit, got.Offset)
	}
}

func TestTMDBListReportsExactFilteredTotalOnlyForCompleteResultSet(t *testing.T) {
	tests := []struct {
		name           string
		items          []models.TrendingItem
		sourceTotal    int
		wantTotal      int
		wantUnfiltered int
	}{
		{
			name: "complete result set",
			items: []models.TrendingItem{
				{Title: models.Title{ID: "released", Name: "Released", MediaType: "movie", Status: models.MovieReleaseStatusReleased}},
				{Title: models.Title{ID: "upcoming", Name: "Upcoming", MediaType: "movie", Status: models.MovieReleaseStatusUpcoming}},
			},
			sourceTotal:    2,
			wantTotal:      1,
			wantUnfiltered: 2,
		},
		{
			name: "partial page",
			items: []models.TrendingItem{
				{Title: models.Title{ID: "released", Name: "Released", MediaType: "movie", Status: models.MovieReleaseStatusReleased}},
			},
			sourceTotal:    100,
			wantTotal:      100,
			wantUnfiltered: 100,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfgManager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			if err := cfgManager.Save(config.Settings{}); err != nil {
				t.Fatalf("save settings: %v", err)
			}
			service := &displayListTMDBMetadata{
				mockMetadataServiceStartup: &mockMetadataServiceStartup{},
				items:                      test.items,
				total:                      test.sourceTotal,
			}
			handler := handlers.NewMetadataHandler(service, cfgManager)
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/lists/tmdb?sourceType=custom&mediaType=movie&limit=500&offset=0&hideUnreleased=true",
				nil,
			)
			rec := httptest.NewRecorder()
			handler.TMDBList(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var response handlers.DiscoverNewResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Total != test.wantTotal || response.UnfilteredTotal != test.wantUnfiltered {
				t.Fatalf(
					"total/unfilteredTotal = %d/%d, want %d/%d",
					response.Total,
					response.UnfilteredTotal,
					test.wantTotal,
					test.wantUnfiltered,
				)
			}
		})
	}
}

func TestTMDBListFillsPageAfterHideUnreleasedFiltering(t *testing.T) {
	items := make([]models.TrendingItem, 0, 26)
	for range 6 {
		items = append(items, models.TrendingItem{
			Title: models.Title{ID: "upcoming", Name: "Upcoming", MediaType: "movie", Status: models.MovieReleaseStatusUpcoming},
		})
	}
	for range 20 {
		items = append(items, models.TrendingItem{
			Title: models.Title{ID: "released", Name: "Released", MediaType: "movie", Status: models.MovieReleaseStatusReleased},
		})
	}
	cfgManager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := cfgManager.Save(config.Settings{}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	service := &displayListTMDBMetadata{
		mockMetadataServiceStartup: &mockMetadataServiceStartup{},
		items:                      items,
		total:                      len(items),
	}
	handler := handlers.NewMetadataHandler(service, cfgManager)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/lists/tmdb?sourceType=production-company&sourceId=420&mediaType=all&limit=20&offset=0&hideUnreleased=true",
		nil,
	)
	rec := httptest.NewRecorder()
	handler.TMDBList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response handlers.DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 20 || response.Total != 20 || response.UnfilteredTotal != 26 {
		t.Fatalf(
			"items/total/unfilteredTotal = %d/%d/%d, want 20/20/26",
			len(response.Items),
			response.Total,
			response.UnfilteredTotal,
		)
	}
	if service.options.Limit != 500 || service.options.Offset != 0 {
		t.Fatalf("source pagination = limit %d offset %d, want 500/0", service.options.Limit, service.options.Offset)
	}
}
