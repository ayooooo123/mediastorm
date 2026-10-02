package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"novastream/models"
	"novastream/services/customlists"

	"github.com/gorilla/mux"
)

type customListPosterMetadata struct {
	fakeMetadataService
}

func (s *customListPosterMetadata) ApplyLocalizedArtwork(_ context.Context, title *models.Title) bool {
	atomic.AddInt32(&s.applyArtworkCalls, 1)
	title.TextPoster = &models.Image{URL: "recovered-text.jpg", Type: "poster"}
	return true
}

func TestCustomListShelfRecoversMissingTextPosters(t *testing.T) {
	lists, err := customlists.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	list, err := lists.CreateList("profile", "Favorites")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []models.WatchlistUpsert{
		{ID: "tmdb:movie:12092", MediaType: "movie", Name: "Alice in Wonderland", PosterURL: "plain.jpg", ExternalIDs: map[string]string{"tmdb": "12092"}},
		{ID: "tmdb:movie:354912", MediaType: "movie", Name: "Coco", PosterURL: "plain-coco.jpg", TextPosterURL: "saved-text.jpg", ExternalIDs: map[string]string{"tmdb": "354912"}},
	} {
		if _, err := lists.AddItem("profile", list.ID, item); err != nil {
			t.Fatal(err)
		}
	}
	h := NewDisplayListHandler(nil, lists, nil)
	for _, tc := range []struct {
		phase   string
		pending bool
		calls   int32
	}{
		{phase: "cards", pending: true},
		{phase: "complete", calls: 1},
		{calls: 1},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			meta := &customListPosterMetadata{}
			h.SetMetadataService(meta)
			query := url.Values{"source": {"mdblist"}, "url": {customListShelfURLPrefix + list.ID}, "shelfPhase": {tc.phase}}
			r := httptest.NewRequest("GET", "/display-list?"+query.Encode(), nil)
			r = mux.SetURLVars(r, map[string]string{"userID": "profile"})
			w := httptest.NewRecorder()
			h.Get(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var response struct {
				Items           []models.TrendingItem `json:"items"`
				MetadataPending bool                  `json:"metadataPending"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.MetadataPending != tc.pending || atomic.LoadInt32(&meta.applyArtworkCalls) != tc.calls {
				t.Fatalf("pending=%v calls=%d", response.MetadataPending, meta.applyArtworkCalls)
			}
			for _, item := range response.Items {
				if item.Title.Name == "Coco" && (item.Title.TextPoster == nil || item.Title.TextPoster.URL != "saved-text.jpg") {
					t.Fatal("persisted text poster must survive a cache miss")
				}
				if item.Title.Name == "Alice in Wonderland" && !tc.pending && (item.Title.TextPoster == nil || item.Title.TextPoster.URL != "recovered-text.jpg") {
					t.Fatal("completion must recover the old item's missing text poster")
				}
			}
		})
	}
}
